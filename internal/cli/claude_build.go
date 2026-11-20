package cli

// Kernel-attested Claude build observation (delivery Phase 2, task 1).
//
// A build qualifies only when the sha256 of its kernel-attested executable
// is in the pinned table below; the table label is the version shown. The
// executable's path, CLAUDE_CODE_EXECPATH and CLAUDE_CODE_VERSION never
// qualify a build, and unknown or untested builds stay legacy with a stable
// reason.
//
// The digest cache is one strict v1 private document (claude-exe-digests.json)
// in the canonical per-user state domain, keyed by the fstat identity of the
// opened executable. It is written only under lockLocalState and only after a
// complete hash whose post-hash identity is unchanged, so a timed-out or
// raced observation caches nothing and the next hook retries.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"runtime"
	"time"
	"unicode/utf8"
)

// claudeQualifiedBuild is one pinned qualified build: the label shown, the
// platform it was qualified on, and the sha256 of its executable.
type claudeQualifiedBuild struct {
	Label    string
	Platform string
	SHA256   string
}

// claudeQualifiedBuilds is the qualified compatibility baseline: Linux x86-64
// Claude Code 2.1.322 is the initial lab baseline. No env var, flag or file
// can add an entry; only _test.go code may extend it, through a helper that
// restores the table with t.Cleanup.
var claudeQualifiedBuilds = []claudeQualifiedBuild{
	{Label: "2.1.322", Platform: "linux/amd64", SHA256: "bfb3703ad5380242328459bc62ada4bde8ae7ba57c6e1eeadb9c00e08874c478"},
}

// claudeQualifiedBuildFor resolves a digest on this platform only: the same
// bytes on another platform are a different, untested build.
func claudeQualifiedBuildFor(sha string) (claudeQualifiedBuild, bool) {
	for _, b := range claudeQualifiedBuilds {
		if b.SHA256 == sha && b.Platform == runtime.GOOS+"/"+runtime.GOARCH {
			return b, true
		}
	}
	return claudeQualifiedBuild{}, false
}

// claudeBuildEvidence is the outcome of one build observation. Identity is
// the decimal "<dev>:<ino>:<size>:<mtime_ns>:<ctime_ns>" of the opened
// executable; Build is the table label, or "" when the build is unknown;
// Reason is one of the fixed strings below, "" when the build qualified.
// Every failure return (a non-empty Reason, or an error) carries only the
// reason: SHA256, Identity and Build are empty. The unqualified outcome is
// not a failure — it carries the attested digest and identity with an empty
// Build.
type claudeBuildEvidence struct {
	SHA256   string
	Identity string
	Build    string
	Reason   string
}

const (
	claudeBuildReasonUnqualified = "claude build digest is not qualified"
	claudeBuildReasonUnreadable  = "claude executable is unreadable"
	claudeBuildReasonExecPath    = "CLAUDE_CODE_EXECPATH names a different executable"
	claudeBuildReasonTimeout     = "claude build observation timed out"
)

// Test seams: claudeBuildHash computes one digest under a deadline, and
// claudeBuildHashBudget bounds one observation's hash window.
var claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

var claudeBuildHashBudget = time.Second

// --- private digest cache: claude-exe-digests.json, strict v1 ---

const (
	claudeExeDigestsName = "claude-exe-digests.json"
	claudeExeDigestsLock = "claude-exe-digests"
	claudeExeDigestsMax  = 16
	claudeExeDigestsLim  = 64 << 10
	// claudeExeDigestsStoreBudget bounds the store's lock wait: an
	// contention-bound or slow lock skips the cache write and keeps the
	// observation evidence.
	claudeExeDigestsStoreBudget = 250 * time.Millisecond
)

var (
	claudeExeIdentityRE = regexp.MustCompile(`^[0-9]{1,20}(:[0-9]{1,20}){4}$`)
	claudeExeDigestRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// claudeExeDigest maps one executable identity to its observed digest.
type claudeExeDigest struct {
	Identity   string    `json:"identity"`
	SHA256     string    `json:"sha256"`
	ObservedAt time.Time `json:"observed_at"`
}

type claudeExeDigestsFile struct {
	Version int               `json:"version"`
	Entries []claudeExeDigest `json:"entries"`
}

func claudeExeDigestsEmpty() claudeExeDigestsFile {
	return claudeExeDigestsFile{Version: 1, Entries: []claudeExeDigest{}}
}

func claudeExeDigestsValidate(doc claudeExeDigestsFile) error {
	if doc.Version != 1 || doc.Entries == nil || len(doc.Entries) > claudeExeDigestsMax {
		return usageErr("invalid claude exe digest document")
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		if !claudeExeIdentityRE.MatchString(e.Identity) || !claudeExeDigestRE.MatchString(e.SHA256) || e.ObservedAt.IsZero() {
			return usageErr("invalid claude exe digest row")
		}
		if seen[e.Identity] {
			return usageErr("duplicate claude exe digest identity")
		}
		seen[e.Identity] = true
	}
	return nil
}

// claudeExeDigestsDecode is the strict reader: exact keys, no duplicates, no
// unknown fields, scalar types and row shape checked before json.Unmarshal
// sees the bytes (encoding/json alone accepts case-folded keys and nulls).
func claudeExeDigestsDecode(raw []byte) (claudeExeDigestsFile, error) {
	fail := func() (claudeExeDigestsFile, error) {
		return claudeExeDigestsEmpty(), usageErr("invalid claude exe digest document")
	}
	if len(raw) == 0 || len(raw) > claudeExeDigestsLim || !utf8.Valid(raw) {
		return fail()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return fail()
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return fail()
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return fail()
		}
		seen[name] = true
		switch name {
		case "version":
			tok, err := d.Token()
			n, ok := tok.(json.Number)
			if err != nil || !ok {
				return fail()
			}
			if v, err := n.Int64(); err != nil || v != 1 {
				return fail()
			}
		case "entries":
			if tok, err := d.Token(); err != nil || tok != json.Delim('[') {
				return fail()
			}
			count := 0
			for d.More() {
				count++
				if count > claudeExeDigestsMax {
					return fail()
				}
				if err := claudeExeDigestRow(d); err != nil {
					return fail()
				}
			}
			if _, err := d.Token(); err != nil {
				return fail()
			}
		default:
			return fail()
		}
	}
	if _, err := d.Token(); err != nil {
		return fail()
	}
	if !seen["version"] || !seen["entries"] {
		return fail()
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return fail()
	}
	var out claudeExeDigestsFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return fail()
	}
	if err := claudeExeDigestsValidate(out); err != nil {
		return claudeExeDigestsEmpty(), err
	}
	return out, nil
}

// claudeExeDigestRow validates one entry object's exact shape.
func claudeExeDigestRow(d *json.Decoder) error {
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return usageErr("invalid claude exe digest row")
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return usageErr("invalid claude exe digest row")
		}
		seen[name] = true
		val, err := d.Token()
		if err != nil {
			return err
		}
		s, ok := val.(string)
		if !ok {
			return usageErr("invalid claude exe digest row")
		}
		switch name {
		case "identity":
			if !claudeExeIdentityRE.MatchString(s) {
				return usageErr("invalid claude exe digest identity")
			}
		case "sha256":
			if !claudeExeDigestRE.MatchString(s) {
				return usageErr("invalid claude exe digest value")
			}
		case "observed_at":
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil || t.IsZero() {
				return usageErr("invalid claude exe digest timestamp")
			}
		default:
			return usageErr("unknown claude exe digest field: " + name)
		}
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	if !seen["identity"] || !seen["sha256"] || !seen["observed_at"] {
		return usageErr("missing claude exe digest field")
	}
	return nil
}

// claudeExeDigestLookup is the lock-free cache read: writes are atomic
// renames under the digests lock, so a reader sees either the old or the new
// document. A missing or invalid document is a miss; the store self-heals it.
func (a *app) claudeExeDigestLookup(identity string) (string, bool) {
	raw, err := a.readLocalState(claudeExeDigestsName)
	if err != nil {
		return "", false
	}
	doc, err := claudeExeDigestsDecode(raw)
	if err != nil {
		return "", false
	}
	for _, e := range doc.Entries {
		if e.Identity == identity {
			return e.SHA256, true
		}
	}
	return "", false
}

// claudeExeDigestStore records one identity→digest mapping under the digests
// lock, replacing any row for that identity and evicting the oldest
// observations beyond the fixed cap. The caller has verified the hash is
// complete and the identity stable. A corrupt stored document is replaced,
// never extended.
func (a *app) claudeExeDigestStore(ctx context.Context, identity, sha string, now time.Time) error {
	if !claudeExeIdentityRE.MatchString(identity) || !claudeExeDigestRE.MatchString(sha) {
		return usageErr("claude exe digest row is not storable")
	}
	// The lock wait is bounded: a timeout skips the cache write and keeps
	// the observation evidence.
	lockCtx, cancel := context.WithTimeout(ctx, claudeExeDigestsStoreBudget)
	defer cancel()
	unlock, err := a.lockLocalState(lockCtx, claudeExeDigestsLock)
	if err != nil {
		return err
	}
	defer unlock()
	doc := claudeExeDigestsEmpty()
	if raw, err := a.readLocalState(claudeExeDigestsName); err == nil {
		if parsed, err := claudeExeDigestsDecode(raw); err == nil {
			doc = parsed
		}
	}
	entries := make([]claudeExeDigest, 0, len(doc.Entries)+1)
	for _, e := range doc.Entries {
		if e.Identity != identity {
			entries = append(entries, e)
		}
	}
	entries = append(entries, claudeExeDigest{Identity: identity, SHA256: sha, ObservedAt: now.UTC()})
	for len(entries) > claudeExeDigestsMax {
		oldest := 0
		for i := 1; i < len(entries); i++ {
			if entries[i].ObservedAt.Before(entries[oldest].ObservedAt) {
				oldest = i
			}
		}
		entries = append(entries[:oldest], entries[oldest+1:]...)
	}
	raw, err := json.Marshal(claudeExeDigestsFile{Version: 1, Entries: entries})
	if err != nil {
		return err
	}
	return a.writeLocalState(claudeExeDigestsName, append(raw, '\n'))
}
