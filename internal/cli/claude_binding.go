package cli

// Passive Claude root registration (claude delivery foundation, task 3;
// binding document v2 in delivery Phase 2, task 2).
//
// The registry is one strict v2 private document (claude-bindings.json) in
// the canonical per-user state domain, mutated only under
// lockLocalState("claude-bindings") with the existing private atomic
// writers. Registration is passive: it performs no hub request, no socket
// write and no Claude-side output. A refused registration leaves the
// stored state (and the legacy hook/Monitor path) untouched.
//
// v2 keys every binding by the profile's pinned ed25519 hub identity, so
// two URL aliases of one hub are one recipient scope. Its optional build
// fields (build, exe_sha256, exe_identity) are reserved for kernel-attested
// build evidence; build-version gating is parked, so nothing writes them yet.
// A v1 document still decodes: its endpoint-keyed bindings are discarded
// (they re-register at the next hook) and its opt-outs are kept; the next
// write emits v2.

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
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/hubid"
)

const (
	claudeBindingsName = "claude-bindings.json"
	claudeLockName     = "claude-bindings"
	claudeBindingsMax  = 16
	claudeOptOutsMax   = 64
	claudeCanaryMax    = 1
	claudeBindingsV2   = 2
)

var (
	errClaudeUnsupported = errors.New("claude session ownership requires a qualified platform; this build stays on the legacy path")
	errClaudeOptOut      = errors.New("claude registration disabled by a durable opt-out; explicit claude enable is required to clear it")
	// errClaudeHubUnpinned is the fixed pending reason for a profile with no
	// canonical pinned hub identity: an endpoint string is never a hub
	// identity, so registration waits for the first ordinary command's pin.
	errClaudeHubUnpinned = errors.New("claude registration needs a pinned hub identity; run a hub-contacting vecnet command for this profile once (for example vecnet --net PROFILE inbox); claude status and claude disable do not pin")

	claudeHubKey   = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,256}$`)
	claudeStartID  = regexp.MustCompile(`^[0-9]{1,32}$`)
	claudeToken    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	claudeDocLimit = 4 << 20
)

// claudeScope is the exact delivery scope a registration claims: the
// canonical Vecnet config and profile, the pinned ed25519 hub identity and
// the endpoint it was reached through, and the caller/recipient pair. It never comes from model-input
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
// PID plus start/boot identity so a reused pid never matches. Build,
// ExeSHA256 and ExeIdentity are the kernel-attested build evidence
// (observeClaudeBuild). They are parked: build-version gating is disabled,
// so registration never writes them; a stored value must still be well
// formed.
type claudeRoot struct {
	ConfigRoot   string `json:"config_root"`
	SessionID    string `json:"session_id"`
	SocketPath   string `json:"socket_path,omitempty"`
	ProcessStart string `json:"process_start"`
	BootID       string `json:"boot_id"`
	Build        string `json:"build,omitempty"`
	ExeSHA256    string `json:"exe_sha256,omitempty"`
	ExeIdentity  string `json:"exe_identity,omitempty"`
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

// claudeBindingsFile is the strict v2 private document holding the binding
// rows, the addressed opt-outs and the native canary record (at most one;
// written only by the canary opt-in).
type claudeBindingsFile struct {
	Version  int             `json:"version"`
	Bindings []claudeBinding `json:"bindings"`
	OptOuts  []claudeScope   `json:"opt_outs"`
	Canary   []claudeScope   `json:"canary"`
}

func claudeDocumentEmpty() claudeBindingsFile {
	return claudeBindingsFile{Version: claudeBindingsV2, Bindings: []claudeBinding{}, OptOuts: []claudeScope{}, Canary: []claudeScope{}}
}

// The v1 document shape, read only to migrate it: its rows carried an
// unattested version string and an endpoint-derived hub identity.
type claudeRootV1 struct {
	ConfigRoot   string `json:"config_root"`
	SessionID    string `json:"session_id"`
	SocketPath   string `json:"socket_path,omitempty"`
	ProcessStart string `json:"process_start"`
	BootID       string `json:"boot_id"`
	Version      string `json:"version"`
	PID          int    `json:"pid"`
	UID          int    `json:"uid"`
}

type claudeBindingV1 struct {
	claudeScope
	claudeRootV1
	Generation string    `json:"generation"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type claudeBindingsFileV1 struct {
	Version  int               `json:"version"`
	Bindings []claudeBindingV1 `json:"bindings"`
	OptOuts  []claudeScope     `json:"opt_outs"`
}

// claudeScopeRecipient mirrors codexRecipient: acting as a peer addresses
// that peer's inbox; the credential's caller authenticates it.
func claudeScopeRecipient(s claudeScope) string {
	if s.As != "" {
		return s.As
	}
	return s.Caller
}

// claudeScopeValid validates a binding, canary or addressed scope: its hub
// identity must be a canonical pinned ed25519 key.
func claudeScopeValid(s claudeScope) error {
	if err := claudeScopeShape(s); err != nil {
		return err
	}
	if !claudeHubPinned(s.HubIdentity) {
		return usageErr("claude scope requires a canonical pinned ed25519 hub identity")
	}
	return nil
}

// claudeOptOutScopeValid also admits the legacy endpoint form (hub identity
// equal to the endpoint), which v1 opt-outs and an opt-out written before a
// profile is pinned carry: an opt-out must never be lost for want of a pin.
func claudeOptOutScopeValid(s claudeScope) error {
	if err := claudeScopeShape(s); err != nil {
		return err
	}
	if s.HubIdentity != s.HubEndpoint && !claudeHubPinned(s.HubIdentity) {
		return usageErr("claude opt-out hub identity is neither pinned nor its endpoint")
	}
	return nil
}

func claudeHubPinned(identity string) bool {
	_, err := hubid.DecodePubkey(identity)
	return err == nil
}

func claudeScopeShape(s claudeScope) error {
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
	// Build evidence is optional (an unqualified or unobserved build still
	// registers and stays legacy) but well formed whenever present, and a
	// label never stands without the digest and identity it came from.
	if r.ExeSHA256 != "" && !claudeExeDigestRE.MatchString(r.ExeSHA256) {
		return usageErr("claude root executable digest is malformed")
	}
	if r.ExeIdentity != "" && !claudeExeIdentityRE.MatchString(r.ExeIdentity) {
		return usageErr("claude root executable identity is malformed")
	}
	if r.Build != "" && (!localRegistryText(r.Build) || r.ExeSHA256 == "" || r.ExeIdentity == "") {
		return usageErr("claude root build label requires its attested executable digest and identity")
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
// (pinned hub identity, recipient) pair. It derives from nothing else: not
// CWD, not time, not labels, not the URL the hub was reached through.
func claudeBindingKey(s claudeScope) string {
	return claudeKeyOver(s.HubIdentity, s.Recipient)
}

// claudeLegacyBindingKey is the same formula over the endpoint: the key
// slice-3 claims were recorded under, when the hub identity was the
// endpoint. Such claims stay visible and resolvable inbox-only; the scope
// can never be admitted again.
func claudeLegacyBindingKey(s claudeScope) string {
	return claudeKeyOver(s.HubEndpoint, s.Recipient)
}

func claudeKeyOver(hub, recipient string) string {
	sum := sha256.Sum256([]byte("vecnet-claude-binding\x00" + hub + "\x00" + recipient))
	return hex.EncodeToString(sum[:])
}

// claudeSameRecipient matches one recipient across URL aliases (same pinned
// identity) and across the legacy endpoint form (same endpoint), so neither
// an alias nor a pre-pin opt-out can be bypassed.
func claudeSameRecipient(a, b claudeScope) bool {
	return a.Recipient == b.Recipient && (a.HubIdentity == b.HubIdentity || a.HubEndpoint == b.HubEndpoint)
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
	if doc.Version != claudeBindingsV2 || doc.Bindings == nil || doc.OptOuts == nil || doc.Canary == nil ||
		len(doc.Bindings) > claudeBindingsMax || len(doc.OptOuts) > claudeOptOutsMax || len(doc.Canary) > claudeCanaryMax {
		return usageErr("unsupported or invalid claude binding document")
	}
	recipients := map[string]bool{}
	endpoints := map[string]bool{}
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
		// One row per recipient under either key form, exactly as
		// claudeSameRecipient matches them.
		key, legacy := claudeBindingKey(b.claudeScope), claudeLegacyBindingKey(b.claudeScope)
		if recipients[key] || endpoints[legacy] {
			return usageErr("duplicate claude recipient in binding document")
		}
		recipients[key], endpoints[legacy] = true, true
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
		if err := claudeOptOutScopeValid(s); err != nil {
			return err
		}
		key := claudeBindingKey(s)
		if opted[key] {
			return usageErr("duplicate claude opt-out in binding document")
		}
		opted[key] = true
	}
	// The canary record (at most one, checked above) must name a pinned
	// peer credential's own inbox: a delegated or principal scope is never
	// stored, so no reader can be talked into admitting one.
	for _, s := range doc.Canary {
		if err := claudeScopeValid(s); err != nil {
			return err
		}
		if !claudeCanaryEligible(s) {
			return usageErr("claude canary record must be a peer credential's own recipient scope")
		}
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
	// The version selects the strict schema; the strict pass then checks
	// every key (including the version's own spelling) against it.
	var head struct {
		Version json.Number `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return doc, usageErr("invalid claude binding document")
	}
	if head.Version.String() == "1" {
		return claudeDocumentDecodeV1(raw)
	}
	if err := claudeStrictShape(raw, reflect.TypeFor[claudeBindingsFile]()); err != nil {
		return doc, err
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

// claudeDocumentDecodeV1 migrates a strict v1 document in memory: its
// bindings are discarded (endpoint-keyed and version-string qualified, they
// re-register at the next hook under the pinned identity) and its opt-outs
// are kept, never cleared. The next write emits v2.
func claudeDocumentDecodeV1(raw []byte) (claudeBindingsFile, error) {
	doc := claudeDocumentEmpty()
	if err := claudeStrictShape(raw, reflect.TypeFor[claudeBindingsFileV1]()); err != nil {
		return doc, err
	}
	var v1 claudeBindingsFileV1
	if err := json.Unmarshal(raw, &v1); err != nil || v1.Version != 1 || v1.Bindings == nil || v1.OptOuts == nil ||
		len(v1.Bindings) > claudeBindingsMax || len(v1.OptOuts) > claudeOptOutsMax {
		return doc, usageErr("unsupported or invalid claude binding document")
	}
	out := claudeDocumentEmpty()
	out.OptOuts = v1.OptOuts
	if err := claudeDocumentValidate(out); err != nil {
		return doc, err
	}
	return out, nil
}

// claudeStrictShape runs the exact-keys structural pass for one schema.
func claudeStrictShape(raw []byte, t reflect.Type) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := claudeJSONValue(d, t); err != nil {
		return usageErr("invalid claude binding document: " + err.Error())
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return usageErr("trailing data in claude binding document")
	}
	return nil
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

// writeClaudeBindingsDocument persists only a document the strict reader
// accepts: a write that would brick every later registration, status,
// deliver or resolve is refused instead, leaving the stored file as it was.
func (a *app) writeClaudeBindingsDocument(doc claudeBindingsFile) error {
	if err := claudeDocumentValidate(doc); err != nil {
		return usageErr("claude binding document refused before write: " + err.Error())
	}
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
// matches profile and URL aliases of the same recipient (and its legacy
// endpoint form) so they cannot bypass it.
func (a *app) setClaudeOptOut(ctx context.Context, scope claudeScope, disabled bool) error {
	if err := claudeOptOutScopeValid(scope); err != nil {
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

// claudeScopeWitnessAt is claudeScopeWitness against the config at the
// scope's own stored path rather than the env-derived one: every config
// derived path (the pin store included) resolves from scope.ConfigPath, so
// the result never depends on this process's VECNET_CONFIG.
func (a *app) claudeScopeWitnessAt(scope claudeScope) error {
	if scope.ConfigPath == "" || !filepath.IsAbs(scope.ConfigPath) {
		return usageErr("claude binding names no absolute config path")
	}
	at := *a
	getenv := a.getenv
	at.getenv = func(k string) string {
		if k == "VECNET_CONFIG" {
			return scope.ConfigPath
		}
		if getenv == nil {
			return ""
		}
		return getenv(k)
	}
	return at.claudeScopeWitness(scope)
}

// claudeScopeWitness ties the scope to the present config: the profile
// must exist there, its hub and pinned hub identity must match, and a
// stored profile witness (peer id or committed enrollment caller) must match
// the claimed caller.
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
	pinned, err := a.claudeHubIdentity(scope.Profile, p)
	if err != nil {
		return err
	}
	if pinned != scope.HubIdentity {
		return usageErr("claude registration hub identity differs from the profile's present pin")
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
// One active owner per exact recipient scope; resume, /clear and a changed
// hub pin are the only generation-changing transitions; everything unknown
// stays pending.
//
// A row matched through its endpoint whose pinned identity differs from the
// scope's (the profile's pin changed at the same endpoint) is re-keyed to the
// present pin with a fresh generation, so its old key and generation fence
// every claim made under them; the ownership rules still apply first.
//
// A scope that matches more than one stored row (its endpoint names one, its
// present pin another: one hub behind two aliases, re-pinned unevenly) is
// refused as a steal attempt. Re-keying either row onto the other's key would
// persist a duplicate recipient, and merging them is never guessed.
func claudeRegisterTransition(doc *claudeBindingsFile, scope claudeScope, root claudeRoot, now time.Time) (claudeBinding, string, error) {
	existing := -1
	for i, b := range doc.Bindings {
		if !claudeSameRecipient(b.claudeScope, scope) {
			continue
		}
		if existing >= 0 {
			return claudeBinding{}, "", usageErr("claude recipient is already registered through another binding of this hub; it cannot be stolen")
		}
		existing = i
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
	repinned := b.HubIdentity != scope.HubIdentity
	switch {
	case sameProcess && b.SessionID == root.SessionID:
		if b.SocketPath != root.SocketPath {
			// Same conversation and process but a different endpoint is
			// neither resume nor clear: it stays pending.
			return claudeBinding{}, "", usageErr("claude endpoint changed without a process or conversation transition; it stays pending")
		}
		if !repinned {
			// Exact same conversation, process and endpoint: idempotent.
			// Every compared field is already equal, so nothing (including
			// the parked, unwritten build fields) is rewritten.
			return b, "", nil
		}
		// Same root under a changed pin: re-key below.
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
	if repinned {
		b.claudeScope = scope
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
	if err := claudeScopeShape(scope); err != nil {
		return none, err
	}
	if !claudeHubPinned(scope.HubIdentity) {
		return none, errClaudeHubUnpinned
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
	// The global-disable refusal fails closed on an unreadable or invalid
	// settings document, exactly as readClaudeStatus treats it: unknown
	// policy state never registers.
	settings, err := a.claudeSettings()
	if err != nil {
		return none, err
	}
	if settings.Disabled {
		return none, errClaudeDisabledGlobal
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
	BindingKey string                  `json:"binding_key"`
	Generation string                  `json:"generation"`
	State      string                  `json:"state"`
	Reason     string                  `json:"reason,omitempty"`
	Admission  claudeSnapshotAdmission `json:"admission"`
	Scope      claudeSnapshotScope     `json:"scope"`
}

type claudeRuntimeSnapshot struct {
	Version  int                     `json:"version"`
	Bindings []claudeSnapshotBinding `json:"bindings"`
}
