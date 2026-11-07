package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/XertroV/vecnet/internal/tui"
)

// Profile is one [profiles.<name>] table of the config file: a hub origin
// and the long-lived credential (peer, principal or operator) used there.
type Profile struct {
	Hub       string `toml:"hub"`
	HubPubkey string `toml:"hub_pubkey,omitempty"`
	Secret    string `toml:"secret"`
	PeerID    string `toml:"peer_id,omitempty"`
	// AllowInsecureHTTP lets Hub be a plain http:// URL on a host that is
	// not loopback or LAN (checkHubURL); set by enrol --allow-insecure-http.
	AllowInsecureHTTP bool `toml:"allow_insecure_http,omitempty"`
}

// Config is the TOML config file:
//
//	operator_profile = "op"
//	[profiles.op]
//	hub = "https://…"
//	secret = "vn_operator_…"
//	[profiles.home]
//	hub = "https://…"
//	secret = "vn_peer_…"
//
// A profile is selected by --net (hook: --profile), else $VECNET_PROFILE.
// A run naming neither is bare (operator.go): refused for an agent, and for
// a human at a terminal it uses operator_profile, which must hold an
// operator credential. Default is the legacy `default` key: still parsed
// so old files load, never written, and it selects nothing.
type Config struct {
	Default         string             `toml:"default,omitempty"`
	OperatorProfile string             `toml:"operator_profile,omitempty"`
	Profiles        map[string]Profile `toml:"profiles"`
}

// configPath resolves the config file: $VECNET_CONFIG, else
// $XDG_CONFIG_HOME/vecnet/config.toml, else $HOME/.config/vecnet/config.toml.
// Everything goes through getenv so tests never touch the real home.
func configPath(getenv func(string) string) (string, error) {
	if p := getenv("VECNET_CONFIG"); p != "" {
		return p, nil
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "vecnet", "config.toml"), nil
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".config", "vecnet", "config.toml"), nil
	}
	return "", usageErr("cannot locate the config file: set VECNET_CONFIG, XDG_CONFIG_HOME or HOME")
}

// cacheDir resolves $XDG_CACHE_HOME/vecnet, else $HOME/.cache/vecnet.
func cacheDir(getenv func(string) string) (string, error) {
	if d := getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "vecnet"), nil
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".cache", "vecnet"), nil
	}
	return "", errors.New("cannot locate the cache dir: set XDG_CACHE_HOME or HOME")
}

// loadConfig reads path; a missing file is an empty Config.
func loadConfig(path string) (Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{Profiles: map[string]Profile{}}, nil
		}
		return Config{}, configParseErr(path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

// saveConfig writes c to path (mode 0600, directory 0700) atomically.
func saveConfig(path string, c Config) error {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeFileAtomic(path, b.Bytes())
}

// profile returns the profile called name, which callers have already
// chosen (--net, $VECNET_PROFILE or operator_profile; see connect).
func (c Config) profile(name string) (string, Profile, error) {
	if name == "" {
		return "", Profile{}, usageErr("no profile named; pass --net <profile>")
	}
	if len(c.Profiles) == 0 {
		return "", Profile{}, usageErr(fmt.Sprintf("no profile %q: no profile is configured; run `vecnet enrol --hub URL --token T --profile NAME`", name))
	}
	p, ok := c.Profiles[name]
	if !ok {
		return "", Profile{}, usageErr(fmt.Sprintf("no profile %q in the config file", name))
	}
	if p.Hub == "" || p.Secret == "" {
		return "", Profile{}, usageErr(fmt.Sprintf("profile %q needs hub and secret", name))
	}
	return name, p, nil
}

// setOperatorProfile rewrites only the root operator_profile value. Comments,
// secrets and every other byte stay as they were. A missing assignment is
// inserted at the top. The named profile has to be in the file read under the
// lock; a missing file, an empty file, or a config that no longer contains
// that profile is left untouched.
//
// The whole reread, check and replace run under the config lock (the same
// path+".lock" editProfiles uses). A symlink is refused, and the file is not
// replaced if its bytes changed under the lock.
func setOperatorProfile(path, name string) error {
	if name == "" {
		return usageErr("operator_profile name is empty")
	}
	quoted, err := profileString(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return setOperatorProfileLocked(path, name, quoted)
}

// operatorProfiles lists profiles that hold an operator credential, by name.
// The secret never leaves this process. A hub URL that fails the check is
// replaced with a fixed label so the picker never prints it. allowEnv is
// VECNET_ALLOW_INSECURE_HTTP, which does not make a secret-bearing URL printable.
func operatorProfiles(c Config, allowEnv bool) []tui.ProfileChoice {
	names := make([]string, 0, len(c.Profiles))
	for name, p := range c.Profiles {
		if isOperatorSecret(p.Secret) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]tui.ProfileChoice, len(names))
	for i, name := range names {
		p := c.Profiles[name]
		hub := "unusable; edit the config"
		if where, ok := printableHub(p.Hub, p.AllowInsecureHTTP || allowEnv); ok {
			hub = where
		}
		out[i] = tui.ProfileChoice{Name: name, Hub: hub}
	}
	return out
}

// writeFileAtomic writes data to a temp file beside path and renames it
// over path, so a reader never sees a half-written file. Mode 0600.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return werr
}

// cache is the CLI's local state under the cache dir: messages.json maps
// message_id → thread_id (filled from inbox, watch and thread output, read
// by reply). peers.json maps peer_id → display name (filled by watch,
// read by inbox). No cursor is cached: the durable cursor is the hub's acked
// cursor, shared by every consumer of a stream (PROTOCOL §2.3).
type cache struct {
	dir string // "" disables the cache
}

// maxCachedMessages bounds messages.json; the oldest ids (ULIDs sort by
// time) are dropped first.
const maxCachedMessages = 10000

func (c cache) readJSON(name string) map[string]string {
	m := map[string]string{}
	if c.dir == "" {
		return m
	}
	raw, err := os.ReadFile(filepath.Join(c.dir, name))
	if err != nil {
		return m
	}
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]string{}
	}
	return m
}

func (c cache) writeJSON(name string, m map[string]string) error {
	if c.dir == "" {
		return errors.New("no cache dir")
	}
	raw, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(c.dir, name), raw)
}

// peerNames reads the last known directory, sanitizing cached values too.
func (c cache) peerNames() map[string]string {
	names := c.readJSON("peers.json")
	for id, name := range names {
		names[id] = peerName(name)
	}
	return names
}

// threadOf returns the cached thread of messageID.
func (c cache) threadOf(messageID string) (string, bool) {
	t, ok := c.readJSON("messages.json")[messageID]
	return t, ok && t != ""
}

// rememberMessages records message_id → thread_id pairs.
func (c cache) rememberMessages(pairs map[string]string) error {
	if len(pairs) == 0 || c.dir == "" {
		return nil
	}
	m := c.readJSON("messages.json")
	changed := false
	for id, thr := range pairs {
		if id != "" && thr != "" && m[id] != thr {
			m[id] = thr
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(m) > maxCachedMessages {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(m)-maxCachedMessages] {
			delete(m, k)
		}
	}
	return c.writeJSON("messages.json", m)
}

// configParseError is a TOML failure that names the file and, when the
// decoder reported one, the line. Decoder text is never kept: it quotes the
// bytes being read, and those bytes can be a credential.
type configParseError struct {
	file string
	line int
}

func (e *configParseError) Error() string {
	if e.line > 0 {
		return fmt.Sprintf("%s: parse error at line %d", e.file, e.line)
	}
	return e.file + ": parse error"
}

func configParseErr(path string, err error) error {
	if err == nil {
		return nil
	}
	file := "config.toml"
	if path != "" {
		file = filepath.Base(path)
	}
	return &configParseError{file: file, line: tomlErrorLine(err)}
}

func tomlErrorLine(err error) int {
	var pe toml.ParseError
	if !errors.As(err, &pe) {
		return 0
	}
	if pe.Position.Line > 0 {
		return pe.Position.Line
	}
	return pe.Line
}
