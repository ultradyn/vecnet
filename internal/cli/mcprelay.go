package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// mcpRelayMaxBodyBytes matches httpapi.MaxBodyBytes without making the CLI
// transport depend on the server package.
const mcpRelayMaxBodyBytes = 256 << 10

// mcpStartupError preserves the relay's specified one-line refusal text.
// All other CLI errors keep their usual formatting and exit codes.
type mcpStartupError struct{ message string }

func (e *mcpStartupError) Error() string { return "vecnet mcp: " + e.message }

type mcpRelayMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// mcpRelay owns one invocation's transport state. It never opens a hint
// stream: SSE is consumed only when it is the response to a POST.
type mcpRelay struct {
	a                *app
	c                *client
	session, version string
	// discovered is the revision a server/discover answered; it is the
	// header of a later stateless request that names none in params._meta.
	discovered string
	initialize []byte
	tools      map[string][]string
	now        func() time.Time
	sleep      func(context.Context, time.Duration) bool
	outage     reconnectStatus
}

func (a *app) cmdMCP(args []string) error {
	if len(args) != 0 {
		return usageErr("usage: vecnet mcp (global --net PROFILE and --as PEER_ID only)")
	}
	c, profile, _, err := a.connect()
	if err != nil {
		// connect's URL/config diagnostics can contain raw userinfo. The relay
		// must not reproduce either config contents or URLs on either stream.
		return usageErr("mcp: cannot resolve profile or hub; check config, --net <profile> or VECNET_PROFILE")
	}
	if isOperatorSecret(c.secret) {
		return &mcpStartupError{"operator credentials stay on the CLI; enrol a peer or principal profile for MCP"}
	}
	if v := a.getenv("VECNET_PROFILE"); v != "" && v != profile {
		return &mcpStartupError{fmt.Sprintf("this session is profile %s; relay for profile %s refuses", v, profile)}
	}
	relay := mcpRelay{a: a, c: c}
	scanner := bufio.NewScanner(a.stdin)
	// Match the hub's 256 KiB request cap, with room for the line delimiter.
	scanner.Buffer(make([]byte, 4096), mcpRelayMaxBodyBytes+2)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		var msg mcpRelayMessage
		if !json.Valid(raw) {
			if err := relay.rpcError(nil, -32700, "Parse error"); err != nil {
				return err
			}
			continue
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.JSONRPC != "2.0" {
			if err := relay.rpcError(nil, -32600, "Invalid Request"); err != nil {
				return err
			}
			continue
		}
		fields, err := mcpRelayFields(raw, "jsonrpc", "id", "method", "params", "result", "error")
		if err != nil {
			if err := relay.replyError(msg.ID, err); err != nil {
				return err
			}
			continue
		}
		// Like the hub, recognize replies to server-initiated requests by
		// an absent method plus an id and result/error. Forward them
		// unchanged, without replying to a response (even on HTTP failure).
		response := fields["method"] == nil && len(msg.ID) > 0 && (len(msg.Result) > 0 || len(msg.Error) > 0)
		if msg.Method == "" && !response {
			if err := relay.rpcError(nil, -32600, "Invalid Request"); err != nil {
				return err
			}
			continue
		}
		err = relay.exchange(raw, msg, !response)
		if err != nil && !response {
			if err := relay.replyError(msg.ID, err); err != nil {
				return err
			}
		}
	}
	if scanner.Err() != nil {
		return usageErr("mcp: cannot read input")
	}
	return nil
}

func (r *mcpRelay) rpcError(id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return r.a.emitValue(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

type mcpBindingError struct{ message string }

func (e *mcpBindingError) Error() string { return e.message }

// replyError never prints transport errors, response bodies, or headers.
func (r *mcpRelay) replyError(id json.RawMessage, err error) error {
	if len(id) == 0 {
		return nil
	} // Notifications have no response, even on failure.
	var binding *mcpBindingError
	if errors.As(err, &binding) {
		return r.rpcError(id, -32602, binding.message)
	}
	var hub *hubError
	if errors.As(err, &hub) {
		code := hub.Code
		// A hub's error code is data too. Only a short identifier is reflected;
		// never reflect a secret or a raw header placed in an error envelope.
		if !mcpRelayCode(code) || (r.c.secret != "" && strings.Contains(code, r.c.secret)) {
			code = "http_" + strconv.Itoa(hub.Status)
		}
		return r.rpcError(id, -32603, fmt.Sprintf("vecnet relay: hub answered %d %s", hub.Status, code))
	}
	return r.rpcError(id, -32603, "vecnet relay: hub unreachable")
}
func mcpRelayCode(code string) bool {
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

var errMCPListenEnded = errors.New("subscription stream ended")

// Local response processing (schema/cache/output) is distinct from reading
// the upstream body, even when a local writer returns EOF or a network error.
type mcpResponseError struct{ err error }

func (e *mcpResponseError) Error() string { return e.err.Error() }
func (e *mcpResponseError) Unwrap() error { return e.err }

type mcpExchange struct {
	status                                *reconnectStatus
	acked, resuming, completed, delivered bool
	hadLive                               bool
}

func (r *mcpRelay) exchange(raw []byte, msg mcpRelayMessage, emit bool) error {
	if r.outage.out == nil {
		r.outage = reconnectStatus{out: r.a.stderr, name: "mcp", now: r.now}
	}
	st := &mcpExchange{status: &r.outage}
	for failures := 1; ; failures++ {
		st.hadLive = false
		err := r.exchangeAttempt(raw, msg, emit, st)
		if err == nil {
			st.status.live()
			return nil
		}
		if r.a.ctx.Err() != nil {
			return r.a.ctx.Err()
		}
		var local *mcpResponseError
		if errors.As(err, &local) {
			return err
		}
		if st.completed {
			var network *netError
			if errors.As(err, &network) {
				// The final answer already reached stdout.
				return nil
			}
			return err
		}
		if !mcpRetryable(msg, err, st.delivered) {
			return err
		}
		if st.hadLive {
			// This acknowledged stream's close starts a new outage.
			failures = 1
		}
		st.resuming = st.acked
		// Never print response bodies, URLs, bearer credentials or raw errors.
		detail := "hub unavailable"
		var hub *hubError
		if errors.As(err, &hub) {
			detail = fmt.Sprintf("hub answered HTTP %d", hub.Status)
		}
		st.status.failed(detail)
		// One-shot calls must release stdin for later requests. Only the
		// explicit subscription owns an indefinite reconnecting lifetime.
		if failures >= 3 && msg.Method != "subscriptions/listen" {
			return err
		}
		pause := r.sleep
		if pause == nil {
			pause = sleepCtx
		}
		if !pause(r.a.ctx, reconnectDelay(failures)) {
			return r.a.ctx.Err()
		}
	}
}

func mcpRetryable(msg mcpRelayMessage, err error, delivered bool) bool {
	// A failed dial sent no request. All later transport failures may have
	// executed a mutation; even a proxy 502/504 can hide a completed write.
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	safe := false
	switch msg.Method {
	case "initialize", "server/discover", "ping", "tools/list", "resources/list", "resources/read", "prompts/list", "prompts/get", "subscriptions/listen":
		safe = true
	}
	if !safe || (delivered && msg.Method != "subscriptions/listen") {
		return false
	}
	var hub *hubError
	if errors.As(err, &hub) {
		return hub.Status == 502 || hub.Status == 503 || hub.Status == 504
	}
	var network *netError
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errMCPListenEnded)
}

// exchangeAttempt retries exactly once after session loss. The cached initialize
// is sent privately; its response updates headers without duplicating the
// client's original initialize response on stdout.
func (r *mcpRelay) exchangeAttempt(raw []byte, msg mcpRelayMessage, emit bool, st *mcpExchange) error {
	// Keep the client's raw request intact: a new catalog may remove a
	// property that the previous catalog caused us to inject.
	wire := raw
	var err error
	if r.a.as != "" {
		if err := r.bindResource(msg); err != nil {
			return err
		}
	}
	if r.a.as != "" && msg.Method == "tools/call" {
		wire, err = r.bind(raw, msg)
		if err != nil {
			return err
		}
	}
	err = r.post(wire, msg, emit, st)
	var hub *hubError
	if msg.Method != "initialize" && len(r.initialize) > 0 && errors.As(err, &hub) && hub.Status == 404 && hub.Code == "session_not_found" {
		r.session, r.version = "", ""
		var init mcpRelayMessage
		_ = json.Unmarshal(r.initialize, &init)
		if err := r.post(r.initialize, init, false, nil); err != nil {
			return err
		}
		if r.version == "" {
			return errors.New("relay reinitialization failed")
		}
		// A restarted hub may advertise different identity properties.
		// Recheck the caller's original arguments under its new catalog.
		if r.a.as != "" && msg.Method == "tools/call" {
			wire, err = r.bind(raw, msg)
			if err != nil {
				return err
			}
		}
		return r.post(wire, msg, emit, st)
	}
	return err
}

func (r *mcpRelay) post(raw []byte, msg mcpRelayMessage, emit bool, st *mcpExchange) error {
	// Binding can add bytes to a legal input line. Check the final body on
	// every path, including unbound requests and session recovery posts.
	if len(raw) > mcpRelayMaxBodyBytes {
		return &mcpBindingError{"relay request exceeds 256 KiB"}
	}
	// MCP POST replies can be SSE streams. Their lifetime belongs to the
	// caller and hub EOF, rather than the ordinary CLI request deadline.
	req, err := r.c.newRequest(r.a.ctx, http.MethodPost, "/mcp", nil, rawBody{r: bytes.NewReader(raw), contentType: "application/json", length: int64(len(raw))})
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	session, version := r.session, r.version
	if mv := mcpRelayMetaVersion(msg.Params); mv != "" && mv != version {
		// A 2026-07-28 request (server/discover included) names its revision
		// in params._meta, which the hub requires the header to equal. It
		// belongs to no session (PROTOCOL §4.1).
		session, version = "", mv
	} else if version == "" {
		version = r.discovered
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	resp, err := r.c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return errPeerDisabled
	}
	handle := func(payload []byte) (err error) {
		defer func() {
			if err != nil {
				err = &mcpResponseError{err: err}
			}
		}()
		var reply mcpRelayMessage
		if err := json.Unmarshal(payload, &reply); err != nil || reply.JSONRPC != "2.0" || (len(reply.Result) == 0 && len(reply.Error) == 0 && reply.Method == "") {
			return errors.New("invalid relay response")
		}
		if st != nil && msg.Method == "subscriptions/listen" && reply.Method == "notifications/subscriptions/acknowledged" {
			st.hadLive = true
			st.status.live()
			if st.acked {
				if st.resuming && emit {
					var p struct {
						Meta          json.RawMessage `json:"_meta"`
						Notifications struct {
							URIs []string `json:"resourceSubscriptions"`
						} `json:"notifications"`
					}
					if json.Unmarshal(reply.Params, &p) != nil {
						return errors.New("invalid subscription acknowledgement")
					}
					slices.Sort(p.Notifications.URIs)
					for _, uri := range slices.Compact(p.Notifications.URIs) {
						params := map[string]any{"uri": uri}
						if len(p.Meta) > 0 {
							params["_meta"] = p.Meta
						}
						if err := r.a.emitValue(map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/updated", "params": params}); err != nil {
							return err
						}
					}
				}
				st.resuming = false
				return nil // the stdio client already received its acknowledgement
			}
			st.acked = true
		}
		final := bytes.Equal(bytes.TrimSpace(reply.ID), bytes.TrimSpace(msg.ID)) && len(reply.ID) > 0
		if st != nil && final {
			st.status.live()
		}
		// Only a response to this request may populate cached transport state.
		if bytes.Equal(bytes.TrimSpace(reply.ID), bytes.TrimSpace(msg.ID)) && len(reply.ID) > 0 && len(reply.Error) == 0 {
			switch msg.Method {
			case "initialize":
				var result struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(reply.Result, &result) == nil && result.ProtocolVersion != "" {
					r.session = resp.Header.Get("Mcp-Session-Id")
					r.version = result.ProtocolVersion
					r.initialize = append(r.initialize[:0], raw...)
					r.tools = nil
				}
			case "server/discover":
				var result struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(reply.Result, &result) == nil && result.ProtocolVersion != "" {
					r.discovered = result.ProtocolVersion
				}
			case "tools/list":
				if err := r.cacheTools(reply.Result); err != nil {
					return err
				}
			case "resources/list":
				if r.a.as != "" {
					bound, err := r.boundResources(payload)
					if err != nil {
						return err
					}
					payload = bound
				}
			}
		}
		if emit && len(msg.ID) > 0 {
			if err := r.a.emitJSON(payload); err != nil {
				return err
			}
			if st != nil {
				st.delivered = true
			}
		}
		if st != nil && final {
			st.completed = true
		}
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		err := mcpRelaySSE(resp.Body, handle)
		if err == nil && msg.Method == "subscriptions/listen" && st != nil && !st.completed {
			return errMCPListenEnded
		}
		return err
	}
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return &netError{err: err}
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	return handle(payload)
}

// mcpRelaySSE joins multi-line data fields within each frame, drops
// comments and event metadata, and emits compact JSON one frame at a time.
// Lines end in CR, LF or CRLF, as for every SSE reader (PROTOCOL §13.1).
func mcpRelaySSE(body io.Reader, handle func([]byte) error) error {
	// Response frames can contain a full inbox page: no token ceiling
	// beyond memory; only requests have a size cap.
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), math.MaxInt)
	sc.Split(mcpRelaySSELines())
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		err := handle([]byte(strings.Join(data, "\n")))
		data = nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field == "data" {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := sc.Err(); err != nil {
		return &netError{err: err}
	}
	return flush()
}

// mcpRelaySSELines splits on CR, LF and CRLF like splitSSELines, but ends a
// line at its CR at once and drops a following LF when it arrives, so a
// live stream's CR-terminated frame is relayed without waiting for the
// next byte.
func mcpRelaySSELines() bufio.SplitFunc {
	skipLF := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		start := 0
		if skipLF && len(data) > 0 {
			skipLF = false
			if data[0] == '\n' {
				start = 1
			}
		}
		i := bytes.IndexAny(data[start:], "\r\n")
		if i < 0 {
			if atEOF && len(data) > start {
				return len(data), data[start:], nil
			}
			return start, nil, nil
		}
		i += start
		skipLF = data[i] == '\r'
		return i + 1, data[start:i], nil
	}
}

// mcpRelayMetaVersion is the revision a request names in params._meta,
// read exactly as the hub reads it (mcpserver.metaVersion), or "".
func mcpRelayMetaVersion(params json.RawMessage) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return ""
	}
	for _, key := range []string{"io.modelcontextprotocol/protocolVersion", "protocolVersion"} {
		if v, ok := p.Meta[key].(string); ok {
			return v
		}
	}
	return ""
}

// mcpRelayInboxURI is the inbox resource of peer id (PROTOCOL §4.2).
func mcpRelayInboxURI(id string) string { return "vecnet://peer/" + id + "/inbox" }

// bindResource holds a --as relay's resource methods to the bound peer's
// own inbox: a principal's other peers and its aggregate inbox are refused
// before anything is forwarded, like a tool argument naming another peer.
func (r *mcpRelay) bindResource(msg mcpRelayMessage) error {
	refused := &mcpBindingError{"relay is bound to --as " + r.a.as}
	want := mcpRelayInboxURI(r.a.as)
	params := msg.Params
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage("{}")
	}
	switch msg.Method {
	case "resources/read", "resources/subscribe", "resources/unsubscribe":
		p, err := mcpRelayFields(params, "uri")
		if err != nil {
			return err
		}
		var uri string
		if json.Unmarshal(p["uri"], &uri) != nil || uri != want {
			return refused
		}
	case "subscriptions/listen":
		p, err := mcpRelayFields(params, "resourceSubscriptions", "notifications")
		if err != nil {
			return err
		}
		lists := []json.RawMessage{p["resourceSubscriptions"]}
		if n := p["notifications"]; len(n) > 0 && string(n) != "null" {
			nested, err := mcpRelayFields(n, "resourceSubscriptions")
			if err != nil {
				return err
			}
			lists = append(lists, nested["resourceSubscriptions"])
		}
		for _, list := range lists {
			if len(list) == 0 || string(list) == "null" {
				continue
			}
			var uris []string
			if json.Unmarshal(list, &uris) != nil {
				return refused
			}
			for _, uri := range uris {
				if uri != want {
					return refused
				}
			}
		}
	}
	return nil
}

// boundResources keeps only the bound peer's inbox in a resources/list
// reply; every other field is relayed as the hub sent it.
func (r *mcpRelay) boundResources(payload []byte) ([]byte, error) {
	invalid := errors.New("invalid resources list")
	var reply, result map[string]json.RawMessage
	var resources []json.RawMessage
	if json.Unmarshal(payload, &reply) != nil || json.Unmarshal(reply["result"], &result) != nil ||
		json.Unmarshal(result["resources"], &resources) != nil {
		return nil, invalid
	}
	want := mcpRelayInboxURI(r.a.as)
	kept := []json.RawMessage{}
	for _, resource := range resources {
		var entry struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(resource, &entry) == nil && entry.URI == want {
			kept = append(kept, resource)
		}
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, resource := range kept {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(resource)
	}
	b.WriteByte(']')
	result["resources"] = b.Bytes()
	reply["result"] = mcpRelayEncodeFields(result)
	return mcpRelayEncodeFields(reply), nil
}

func (r *mcpRelay) cacheTools(raw json.RawMessage) error {
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Tools == nil {
		return errors.New("invalid tools list")
	}
	tools := make(map[string][]string, len(result.Tools))
	for _, tool := range result.Tools {
		fields := []string{}
		for _, field := range []string{"as", "peer"} {
			if _, ok := tool.InputSchema.Properties[field]; ok {
				fields = append(fields, field)
			}
		}
		if tool.Name == "vecnet_threads" {
			fields = append(fields, "participant")
		}
		tools[tool.Name] = fields
	}
	r.tools = tools
	return nil
}

func (r *mcpRelay) bind(raw []byte, msg mcpRelayMessage) ([]byte, error) {
	p, err := mcpRelayFields(msg.Params, "name", "arguments")
	if err != nil {
		return nil, &mcpBindingError{"invalid tools/call params"}
	}
	var name string
	if err := json.Unmarshal(p["name"], &name); err != nil {
		return nil, &mcpBindingError{"invalid tools/call params"}
	}
	if r.tools == nil {
		// Private ids cannot collide on the wire: these responses are consumed
		// here and are never emitted to the stdio client.
		params := `{}`
		if mv := mcpRelayMetaVersion(msg.Params); mv != "" {
			// A stateless caller's catalogue is fetched in its own revision.
			encoded, _ := json.Marshal(mv)
			params = `{"_meta":{"io.modelcontextprotocol/protocolVersion":` + string(encoded) + `}}`
		}
		list := []byte(`{"jsonrpc":"2.0","id":"vecnet-relay-tools","method":"tools/list","params":` + params + `}`)
		var msg mcpRelayMessage
		_ = json.Unmarshal(list, &msg)
		if err := r.exchange(list, msg, false); err != nil {
			return nil, err
		}
		if r.tools == nil {
			return nil, errors.New("tools list unavailable")
		}
	}
	fields := r.tools[name]
	if len(fields) == 0 {
		return raw, nil
	}
	var arguments map[string]json.RawMessage
	if len(p["arguments"]) > 0 && string(p["arguments"]) != "null" {
		if err := json.Unmarshal(p["arguments"], &arguments); err != nil {
			return nil, &mcpBindingError{"invalid tool arguments"}
		}
	}
	if arguments == nil {
		arguments = map[string]json.RawMessage{}
	}
	for _, field := range fields {
		if value, ok := arguments[field]; ok {
			var got string
			if err := json.Unmarshal(value, &got); err != nil || (got != r.a.as && !(field == "participant" && got == "me")) {
				return nil, &mcpBindingError{"relay is bound to --as " + r.a.as}
			}
		}
		arguments[field], _ = json.Marshal(r.a.as)
	}
	p["arguments"] = mcpRelayEncodeFields(arguments)
	var request map[string]json.RawMessage
	_ = json.Unmarshal(raw, &request)
	request["params"] = mcpRelayEncodeFields(p)
	return mcpRelayEncodeFields(request), nil
}

// mcpRelayFields rejects aliases and duplicate routing fields before a map
// can hide them. The hub's struct decoder accepts Unicode case folds (such
// as argumentſ), whereas map keys are exact; forwarding both would let the
// hub select a different tool or arguments from the ones we bound.
func mcpRelayFields(raw json.RawMessage, routing ...string) (map[string]json.RawMessage, error) {
	invalid := &mcpBindingError{"invalid relay request fields"}
	d := json.NewDecoder(bytes.NewReader(raw))
	if start, err := d.Token(); err != nil || start != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, invalid
		}
		key, ok := token.(string)
		if !ok {
			return nil, invalid
		}
		for _, name := range routing {
			if strings.EqualFold(key, name) {
				if _, duplicate := fields[name]; key != name || duplicate {
					return nil, invalid
				}
			}
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, invalid
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, invalid
	}
	return fields, nil
}

// Like mcpserver.encodeFields, keep raw values byte for byte: json.Marshal
// expands <, >, &, and U+2028/9 even inside RawMessage. SetEscapeHTML(false)
// still rewrites U+2028/9. Expansion can push a legal request over the cap.
func mcpRelayEncodeFields(fields map[string]json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range slices.Sorted(maps.Keys(fields)) {
		if i > 0 {
			b.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		b.Write(encoded)
		b.WriteByte(':')
		b.Write(fields[key])
	}
	b.WriteByte('}')
	return b.Bytes()
}
