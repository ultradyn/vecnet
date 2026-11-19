package cli

// Passive Claude root registration (claude delivery foundation, task 3).
//
// The registry is one strict v1 private document (claude-bindings.json) in
// the canonical per-user state domain, mutated only under
// lockLocalState("claude-bindings") with the existing private atomic
// writers. Registration is passive: it performs no hub request, no socket
// write and no Claude-side output. A refused registration leaves the
// stored state (and the legacy hook/Monitor path) untouched.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	claudeBindingsName = "claude-bindings.json"
	claudeLockName     = "claude-bindings"
	claudeBindingsMax  = 16
	claudeOptOutsMax   = 64
)

var (
	errClaudeUnsupported = errors.New("claude session ownership requires a qualified platform; this build stays on the legacy path")
	errClaudeOptOut      = errors.New("claude registration disabled by a durable opt-out; explicit claude enable is required to clear it")

	claudeHubKey   = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,256}$`)
	claudeStartID  = regexp.MustCompile(`^[0-9]{1,32}$`)
	claudeToken    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	claudeDocLimit = 4 << 20
)

// claudeScope is the exact delivery scope a registration claims: the
// canonical Vecnet config and profile, the verified hub identity and
// endpoint, and the caller/recipient pair. It never comes from model-input
// strings or a profile name alone; registerClaudeRoot re-checks it against
// the present config before committing.
type claudeScope struct {
	ConfigPath  string `json:"config_path"`
	Profile     string `json:"profile"`
	As          string `json:"as,omitempty"`
	HubIdentity string `json:"hub_identity"`
	HubEndpoint string `json:"hub_endpoint"`
	Caller      string `json:"caller"`
	Recipient   string `json:"recipient"`
}

// claudeRoot anchors one Claude Code process and conversation on the local
// host: canonical config root, session id, private messaging endpoint, and
// PID plus start/boot identity so a reused pid never matches.
type claudeRoot struct {
	ConfigRoot   string `json:"config_root"`
	SessionID    string `json:"session_id"`
	SocketPath   string `json:"socket_path,omitempty"`
	ProcessStart string `json:"process_start"`
	BootID       string `json:"boot_id"`
	Version      string `json:"version"`
	PID          int    `json:"pid"`
	UID          int    `json:"uid"`
}

// claudeBinding embeds the scope and root coordinates plus the ownership
// generation that fences later writers.
type claudeBinding struct {
	claudeScope
	claudeRoot
	Generation string    `json:"generation"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// claudeBindingsFile is the strict v1 private document holding both the
// binding rows and the addressed opt-outs.
type claudeBindingsFile struct {
	Version  int             `json:"version"`
	Bindings []claudeBinding `json:"bindings"`
	OptOuts  []claudeScope   `json:"opt_outs"`
}

func claudeDocumentEmpty() claudeBindingsFile {
	return claudeBindingsFile{Version: 1, Bindings: []claudeBinding{}, OptOuts: []claudeScope{}}
}

// claudeScopeRecipient mirrors codexRecipient: acting as a peer addresses
// that peer's inbox; the credential's caller authenticates it.
func claudeScopeRecipient(s claudeScope) string {
	if s.As != "" {
		return s.As
	}
	return s.Caller
}

func claudeScopeValid(s claudeScope) error {
	if !localRegistryPath(s.ConfigPath) {
		return usageErr("claude scope requires a clean absolute config path")
	}
	if s.Profile == "" || !localRegistryText(s.Profile) {
		return usageErr("claude scope requires a usable profile name")
	}
	if s.As != "" && !hookID.MatchString(s.As) {
		return usageErr("claude scope delegation must name a peer")
	}
	if !claudeHubKey.MatchString(s.HubEndpoint) || normalizeHub(s.HubEndpoint) != s.HubEndpoint {
		return usageErr("claude scope requires a canonical hub endpoint")
	}
	// v1 pins the hub identity to the normalized endpoint; a separate
	// pinned-key identity arrives with host-side verification, never from
	// an input string.
	if s.HubIdentity != s.HubEndpoint {
		return usageErr("claude scope hub identity does not match its endpoint")
	}
	if !hookID.MatchString(s.Caller) {
		return usageErr("claude scope caller is not a verified identity")
	}
	if !hookID.MatchString(s.Recipient) || claudeScopeRecipient(s) != s.Recipient {
		return usageErr("claude scope recipient must be its delegation or caller")
	}
	return nil
}

// claudeRootShape validates the root's stored form on every platform; the
// platform files add live process evidence.
func claudeRootShape(r claudeRoot) error {
	if !localRegistryPath(r.ConfigRoot) {
		return usageErr("claude root requires a clean absolute config root")
	}
	if !hookSessionID.MatchString(r.SessionID) {
		return usageErr("claude root requires a session id")
	}
	if r.SocketPath != "" && !localRegistryPath(r.SocketPath) {
		return usageErr("claude root socket path must be a clean absolute path")
	}
	if !claudeStartID.MatchString(r.ProcessStart) {
		return usageErr("claude root requires a process start identity")
	}
	if !codexThreadID.MatchString(r.BootID) {
		return usageErr("claude root requires a boot identity")
	}
	if r.Version == "" || !localRegistryText(r.Version) {
		return usageErr("claude root requires the observed build version")
	}
	if r.PID <= 1 {
		return usageErr("claude root requires a real process")
	}
	if r.UID < 0 {
		return usageErr("claude root requires an OS user")
	}
	return nil
}

func claudeGeneration() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// claudeBindingKey is the stable opaque local key for one
// (hub identity, recipient) pair. It derives from nothing else: not CWD,
// not time, not labels.
func claudeBindingKey(s claudeScope) string {
	sum := sha256.Sum256([]byte("vecnet-claude-binding\x00" + s.HubIdentity + "\x00" + s.Recipient))
	return hex.EncodeToString(sum[:])
}

func claudeSameRecipient(a, b claudeScope) bool {
	return a.HubIdentity == b.HubIdentity && a.Recipient == b.Recipient
}

// claudeJSONFields flattens embedded coordinate structs for strict decoding.
func claudeJSONFields(t reflect.Type) (map[string]reflect.Type, map[string]bool) {
	fields := map[string]reflect.Type{}
	required := map[string]bool{}
	var add func(reflect.Type)
	add = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				add(f.Type)
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields[name] = f.Type
			if !strings.Contains(opts, "omitempty") {
				required[name] = true
			}
		}
	}
	add(t)
	return fields, required
}

// claudeJSONValue validates exact keys, duplicates, scalar types and
// required fields before decoding; encoding/json alone accepts case-folded
// keys and null scalars.
func claudeJSONValue(d *json.Decoder, t reflect.Type) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if t == reflect.TypeFor[time.Time]() {
		if _, ok := token.(string); !ok {
			return usageErr("invalid claude binding timestamp")
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return usageErr("invalid claude binding object")
		}
		fields, required := claudeJSONFields(t)
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return usageErr("invalid claude binding key")
			}
			f, known := fields[name]
			if !known || seen[name] {
				return usageErr("unknown or duplicate claude binding field: " + name)
			}
			seen[name] = true
			delete(required, name)
			if err := claudeJSONValue(d, f); err != nil {
				return err
			}
		}
		if len(required) != 0 {
			return usageErr("missing required claude binding field")
		}
		_, err = d.Token()
		return err
	case reflect.Slice:
		if token != json.Delim('[') {
			return usageErr("invalid claude binding array")
		}
		n := 0
		for d.More() {
			n++
			if n > claudeOptOutsMax {
				return usageErr("too many claude binding entries")
			}
			if err := claudeJSONValue(d, t.Elem()); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case reflect.String:
		if _, ok := token.(string); !ok {
			return usageErr("invalid claude binding string")
		}
		return nil
	case reflect.Int:
		n, ok := token.(json.Number)
		if !ok {
			return usageErr("invalid claude binding integer")
		}
		if _, err := n.Int64(); err != nil {
			return err
		}
		return nil
	default:
		return usageErr("unsupported claude binding field")
	}
}

func claudeDocumentValidate(doc claudeBindingsFile) error {
	if doc.Version != 1 || doc.Bindings == nil || doc.OptOuts == nil ||
		len(doc.Bindings) > claudeBindingsMax || len(doc.OptOuts) > claudeOptOutsMax {
		return usageErr("unsupported or invalid claude binding document")
	}
	recipients := map[string]bool{}
	processes := map[string]bool{}
	conversations := map[string]bool{}
	for _, b := range doc.Bindings {
		if err := claudeScopeValid(b.claudeScope); err != nil {
			return err
		}
		if err := claudeRootShape(b.claudeRoot); err != nil {
			return err
		}
		if !claudeToken.MatchString(b.Generation) || b.UpdatedAt.IsZero() {
			return usageErr("invalid claude binding row")
		}
		key := claudeBindingKey(b.claudeScope)
		if recipients[key] {
			return usageErr("duplicate claude recipient in binding document")
		}
		recipients[key] = true
		proc := fmt.Sprintf("%d\x00%s\x00%s", b.PID, b.ProcessStart, b.BootID)
		if processes[proc] {
			return usageErr("duplicate claude process in binding document")
		}
		processes[proc] = true
		convo := b.ConfigRoot + "\x00" + b.SessionID
		if conversations[convo] {
			return usageErr("duplicate claude conversation in binding document")
		}
		conversations[convo] = true
	}
	opted := map[string]bool{}
	for _, s := range doc.OptOuts {
		if err := claudeScopeValid(s); err != nil {
			return err
		}
		key := claudeBindingKey(s)
		if opted[key] {
			return usageErr("duplicate claude opt-out in binding document")
		}
		opted[key] = true
	}
	return nil
}

func claudeDocumentDecode(raw []byte) (claudeBindingsFile, error) {
	doc := claudeDocumentEmpty()
	if len(raw) == 0 || len(raw) > claudeDocLimit {
		return doc, usageErr("invalid claude binding document")
	}
	if !utf8.Valid(raw) {
		return doc, usageErr("claude binding document is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := claudeJSONValue(d, reflect.TypeFor[claudeBindingsFile]()); err != nil {
		return doc, usageErr("invalid claude binding document: " + err.Error())
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return doc, usageErr("trailing data in claude binding document")
	}
	var out claudeBindingsFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return doc, usageErr("invalid claude binding document")
	}
	if err := claudeDocumentValidate(out); err != nil {
		return doc, err
	}
	return out, nil
}

// readClaudeBindingsDocument is the strict reader; the caller holds the
// claude-bindings lock (readClaudeBindings and every mutation do).
func (a *app) readClaudeBindingsDocument() (claudeBindingsFile, error) {
	raw, err := a.readLocalState(claudeBindingsName)
	if errors.Is(err, os.ErrNotExist) {
		return claudeDocumentEmpty(), nil
	}
	if err != nil {
		return claudeBindingsFile{}, err
	}
	return claudeDocumentDecode(raw)
}

func (a *app) writeClaudeBindingsDocument(doc claudeBindingsFile) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(raw) > claudeDocLimit {
		return usageErr("claude binding document exceeds 4 MiB")
	}
	return a.writeLocalState(claudeBindingsName, append(raw, '\n'))
}

// readClaudeBindings returns every stored binding after strict validation.
func (a *app) readClaudeBindings() ([]claudeBinding, error) {
	ctx, cancel := context.WithTimeout(a.localRegistryContext(), 2*time.Second)
	defer cancel()
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return nil, err
	}
	defer unlock()
	doc, err := a.readClaudeBindingsDocument()
	if err != nil {
		return nil, err
	}
	return doc.Bindings, nil
}

// setClaudeOptOut latches (disabled=true) or clears (disabled=false) the
// addressed opt-out for one canonical (hub identity, recipient) pair. It
// grants no ownership, is never cleared by registration or repair, and
// matches profile aliases of the same recipient so they cannot bypass it.
func (a *app) setClaudeOptOut(ctx context.Context, scope claudeScope, disabled bool) error {
	if err := claudeScopeValid(scope); err != nil {
		return err
	}
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return err
	}
	defer unlock()
	doc, err := a.readClaudeBindingsDocument()
	if err != nil {
		return err
	}
	if disabled {
		if slices.ContainsFunc(doc.OptOuts, func(s claudeScope) bool { return claudeSameRecipient(s, scope) }) {
			return nil
		}
		doc.OptOuts = append(doc.OptOuts, scope)
	} else {
		doc.OptOuts = slices.DeleteFunc(doc.OptOuts, func(s claudeScope) bool { return claudeSameRecipient(s, scope) })
	}
	if doc.OptOuts == nil {
		doc.OptOuts = []claudeScope{}
	}
	return a.writeClaudeBindingsDocument(doc)
}

// claudeInvocationChecks cross-checks the invoking environment. CLAUDE_PID
// and CLAUDE_CODE_MESSAGING_SOCKET are consistency evidence only: proof of
// parentage and start identity comes from the platform checks. A child
// agent (hook payload agent_id, surfaced by the hook path as
// CLAUDE_AGENT_ID) never registers its parent session.
func claudeInvocationChecks(getenv func(string) string, root claudeRoot) error {
	if id := strings.TrimSpace(getenv("CLAUDE_AGENT_ID")); id != "" {
		return usageErr("claude child agent (" + id + ") cannot register session ownership")
	}
	if raw := strings.TrimSpace(getenv("CLAUDE_PID")); raw != "" {
		if pid, err := strconv.Atoi(raw); err == nil && pid > 1 && pid != root.PID {
			return usageErr("CLAUDE_PID contradicts the claimed claude process")
		}
	}
	if sock := getenv("CLAUDE_CODE_MESSAGING_SOCKET"); sock != "" && root.SocketPath != "" && sock != root.SocketPath {
		return usageErr("CLAUDE_CODE_MESSAGING_SOCKET contradicts the claimed claude endpoint")
	}
	return nil
}

// claudeScopeWitness ties the scope to the present config: the profile
// must exist there, its hub must match, and a stored profile witness
// (peer id or committed enrollment caller) must match the claimed caller.
// Scope never comes from a profile name alone, and a profile with no
// witness at all cannot witness anything: shape-only matches refuse.
func (a *app) claudeScopeWitness(scope claudeScope) error {
	present, err := configPath(a.getenv)
	if err != nil {
		return err
	}
	present, err = canonicalLocalSetupPath(present)
	if err != nil {
		return err
	}
	if present != scope.ConfigPath {
		return usageErr("claude registration scope does not address the present config")
	}
	cfg, err := loadConfig(scope.ConfigPath)
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[scope.Profile]
	if !ok {
		return usageErr("claude registration profile is absent from the present config")
	}
	if normalizeHub(p.Hub) != scope.HubEndpoint {
		return usageErr("claude registration hub differs from the present config")
	}
	if p.PeerID == "" && p.EnrollmentCaller == "" {
		return usageErr("claude registration requires a stored caller witness in the present config")
	}
	if p.PeerID != "" && p.PeerID != scope.Caller {
		return usageErr("claude registration caller differs from the stored profile witness")
	}
	if p.EnrollmentCaller != "" && p.EnrollmentCaller != scope.Caller {
		return usageErr("claude registration caller differs from the committed enrollment witness")
	}
	return nil
}

// claudeRegisterTransition applies the ownership rules under the lock.
// One active owner per exact recipient scope; resume and /clear are the
// only generation-changing transitions; everything unknown stays pending.
func claudeRegisterTransition(doc *claudeBindingsFile, scope claudeScope, root claudeRoot, now time.Time) (claudeBinding, string, error) {
	existing := -1
	for i, b := range doc.Bindings {
		if claudeSameRecipient(b.claudeScope, scope) {
			existing = i
			break
		}
	}
	if existing < 0 {
		// A fresh claim must not collide with another recipient's process
		// or conversation, whatever profile it arrived through.
		for _, b := range doc.Bindings {
			if b.PID == root.PID && b.ProcessStart == root.ProcessStart && b.BootID == root.BootID {
				return claudeBinding{}, "", usageErr("claude process is already owned by another recipient")
			}
			if b.ConfigRoot == root.ConfigRoot && b.SessionID == root.SessionID {
				return claudeBinding{}, "", usageErr("claude conversation is already owned by another recipient")
			}
		}
		generation, err := claudeGeneration()
		if err != nil {
			return claudeBinding{}, "", err
		}
		b := claudeBinding{claudeScope: scope, claudeRoot: root, Generation: generation, UpdatedAt: now}
		doc.Bindings = append(doc.Bindings, b)
		return b, "registered", nil
	}
	b := doc.Bindings[existing]
	sameProcess := b.ConfigRoot == root.ConfigRoot && b.PID == root.PID && b.ProcessStart == root.ProcessStart && b.BootID == root.BootID && b.UID == root.UID
	switch {
	case sameProcess && b.SessionID == root.SessionID:
		if b.SocketPath != root.SocketPath {
			// Same conversation and process but a different endpoint is
			// neither resume nor clear: it stays pending.
			return claudeBinding{}, "", usageErr("claude endpoint changed without a process or conversation transition; it stays pending")
		}
		// Exact same conversation, process and endpoint: idempotent. A
		// newer observed build version renews metadata without a new
		// generation.
		if b.Version != root.Version {
			b.claudeRoot = root
			b.UpdatedAt = now
			doc.Bindings[existing] = b
			return b, "renewed", nil
		}
		return b, "", nil
	case sameProcess && b.SessionID != root.SessionID:
		// /clear: a verified root lifecycle observation (the session
		// registry under the same live process reports the new id).
		b.claudeRoot = root
	case b.ConfigRoot == root.ConfigRoot && b.SessionID == root.SessionID && b.PID != root.PID:
		// --resume: same conversation, fresh process and endpoint; the old
		// owner must be fenced first.
		if !claudeProcessFenced(b.PID, b.ProcessStart, b.BootID) {
			return claudeBinding{}, "", usageErr("claude resume requires the previous process fenced; it is still live")
		}
		b.claudeRoot = root
	case b.ConfigRoot != root.ConfigRoot || b.SessionID != root.SessionID:
		return claudeBinding{}, "", usageErr("claude recipient is already registered through another live root; it cannot be stolen")
	default:
		return claudeBinding{}, "", usageErr("claude registration matches no known transition for this recipient; it stays pending")
	}
	generation, err := claudeGeneration()
	if err != nil {
		return claudeBinding{}, "", err
	}
	b.Generation, b.UpdatedAt = generation, now
	doc.Bindings[existing] = b
	return b, "replaced", nil
}

// registerClaudeRoot passively records that one exact Claude process and
// conversation owns deliveries for one recipient scope. It performs no hub
// request and no socket write; every refusal leaves the registry, and the
// legacy hook/Monitor path, unchanged.
func (a *app) registerClaudeRoot(ctx context.Context, scope claudeScope, root claudeRoot) (claudeBinding, error) {
	var none claudeBinding
	if err := claudeScopeValid(scope); err != nil {
		return none, err
	}
	if err := claudeRootShape(root); err != nil {
		return none, err
	}
	if err := claudeInvocationChecks(a.getenv, root); err != nil {
		return none, err
	}
	if err := validateClaudeRoot(root); err != nil {
		return none, err
	}
	if err := claudeRootAncestry(root); err != nil {
		return none, err
	}
	unlock, err := a.lockLocalState(ctx, claudeLockName)
	if err != nil {
		return none, err
	}
	defer unlock()
	// Revalidate provenance, registry and the stored profile witness under
	// the lock before committing.
	if err := validateClaudeRoot(root); err != nil {
		return none, err
	}
	if err := claudeRootAncestry(root); err != nil {
		return none, err
	}
	if err := a.claudeScopeWitness(scope); err != nil {
		return none, err
	}
	doc, err := a.readClaudeBindingsDocument()
	if err != nil {
		return none, err
	}
	if slices.ContainsFunc(doc.OptOuts, func(s claudeScope) bool { return claudeSameRecipient(s, scope) }) {
		return none, errClaudeOptOut
	}
	binding, transition, err := claudeRegisterTransition(&doc, scope, root, time.Now().UTC())
	if err != nil {
		return none, err
	}
	if transition == "" {
		return binding, nil
	}
	if err := a.writeClaudeBindingsDocument(doc); err != nil {
		return none, err
	}
	return binding, nil
}

// --- read-only runtime snapshot for the shared delivery host ---

type claudeSnapshotBinding struct {
	BindingKey string `json:"binding_key"`
	Generation string `json:"generation"`
}

type claudeRuntimeSnapshot struct {
	Version  int                     `json:"version"`
	Bindings []claudeSnapshotBinding `json:"bindings"`
}

// cmdClaude hosts the claude connector surface. runtime-snapshot is the
// private host child: a versioned, redacted, read-only view of the private
// registry. It contains only opaque keys and generations, never socket
// paths, config contents or credentials, and never parses the full
// registry host-side.
func (a *app) cmdClaude(args []string) error {
	if len(args) == 0 || args[0] != "runtime-snapshot" {
		return usageErr("claude: expected runtime-snapshot")
	}
	if len(args) > 1 {
		return usageErr("claude runtime-snapshot: unexpected arguments")
	}
	rows, err := a.readClaudeBindings()
	if err != nil {
		return err
	}
	snapshot := claudeRuntimeSnapshot{Version: 1, Bindings: make([]claudeSnapshotBinding, 0, len(rows))}
	for _, b := range rows {
		snapshot.Bindings = append(snapshot.Bindings, claudeSnapshotBinding{BindingKey: claudeBindingKey(b.claudeScope), Generation: b.Generation})
	}
	return a.emitValue(snapshot)
}
