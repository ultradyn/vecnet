package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	initialize       []byte
	tools            map[string][]string
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
		if err := json.Unmarshal(raw, &msg); err != nil || msg.JSONRPC != "2.0" || msg.Method == "" {
			if err := relay.rpcError(nil, -32600, "Invalid Request"); err != nil {
				return err
			}
			continue
		}
		if _, err := mcpRelayFields(raw, "jsonrpc", "id", "method", "params", "result", "error"); err != nil {
			if err := relay.replyError(msg.ID, err); err != nil {
				return err
			}
			continue
		}
		err = relay.exchange(raw, msg, true)
		if err != nil {
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

// exchange retries exactly once after session loss. The cached initialize
// is sent privately; its response updates headers without duplicating the
// client's original initialize response on stdout.
func (r *mcpRelay) exchange(raw []byte, msg mcpRelayMessage, emit bool) error {
	// Keep the client's raw request intact: a new catalog may remove a
	// property that the previous catalog caused us to inject.
	wire := raw
	var err error
	if r.a.as != "" && msg.Method == "tools/call" {
		wire, err = r.bind(raw, msg.Params)
		if err != nil {
			return err
		}
	}
	err = r.post(wire, msg, emit)
	var hub *hubError
	if msg.Method != "initialize" && len(r.initialize) > 0 && errors.As(err, &hub) && hub.Status == 404 && hub.Code == "session_not_found" {
		r.session, r.version = "", ""
		var init mcpRelayMessage
		_ = json.Unmarshal(r.initialize, &init)
		if err := r.post(r.initialize, init, false); err != nil {
			return err
		}
		if r.version == "" {
			return errors.New("relay reinitialization failed")
		}
		// A restarted hub may advertise different identity properties.
		// Recheck the caller's original arguments under its new catalog.
		if r.a.as != "" && msg.Method == "tools/call" {
			wire, err = r.bind(raw, msg.Params)
			if err != nil {
				return err
			}
		}
		return r.post(wire, msg, emit)
	}
	return err
}

func (r *mcpRelay) post(raw []byte, msg mcpRelayMessage, emit bool) error {
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
	if r.session != "" {
		req.Header.Set("Mcp-Session-Id", r.session)
	}
	if r.version != "" {
		req.Header.Set("MCP-Protocol-Version", r.version)
	}
	resp, err := r.c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	handle := func(payload []byte) error {
		var reply mcpRelayMessage
		if err := json.Unmarshal(payload, &reply); err != nil || reply.JSONRPC != "2.0" || (len(reply.Result) == 0 && len(reply.Error) == 0 && reply.Method == "") {
			return errors.New("invalid relay response")
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
			case "tools/list":
				if err := r.cacheTools(reply.Result); err != nil {
					return err
				}
			}
		}
		if emit && len(msg.ID) > 0 {
			return r.a.emitJSON(payload)
		}
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return mcpRelaySSE(resp.Body, handle)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	return handle(payload)
}

// mcpRelaySSE joins multi-line data fields within each frame, drops
// comments and event metadata, and emits compact JSON one frame at a time.
func mcpRelaySSE(body io.Reader, handle func([]byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		err := handle([]byte(strings.Join(data, "\n")))
		data = nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
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
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
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

func (r *mcpRelay) bind(raw, params json.RawMessage) ([]byte, error) {
	p, err := mcpRelayFields(params, "name", "arguments")
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
		list := []byte(`{"jsonrpc":"2.0","id":"vecnet-relay-tools","method":"tools/list","params":{}}`)
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
