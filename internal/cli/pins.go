package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

// Pins are local trust state, separate from the operator's handwritten config.
type pinRecord struct {
	Hub, Pubkey, PinnedAt, How string
	TofuTriedAt                string      `json:",omitempty"`
	LastMove                   *moveRecord `json:",omitempty"`
}
type moveRecord struct{ From, To, NoticeID, IssuedAt string }

func pinsPath(getenv func(string) string) (string, error) {
	path, err := configPath(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "state", "pins.json"), nil
}
func readPins(path string) (map[string]pinRecord, error) {
	m := map[string]pinRecord{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]pinRecord{}
	}
	return m, nil
}
func (a *app) updatePins(fn func(map[string]pinRecord) error) error {
	path, err := pinsPath(a.getenv)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	unlock, err := lockPath(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	records, err := readPins(path)
	if err != nil {
		return err
	}
	if err = fn(records); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}
func (a *app) effectivePin(name string, p Profile) (pubkey, how string) {
	hub, err := hubid.NormalizeURL(p.Hub)
	if err != nil {
		return p.HubPubkey, "config"
	}
	path, err := pinsPath(a.getenv)
	var r pinRecord
	if err == nil {
		if records, e := readPins(path); e == nil {
			r = records[name]
		}
	}
	if p.HubPubkey != "" {
		if r.Hub == hub && r.Pubkey == p.HubPubkey && r.How != "" {
			return p.HubPubkey, r.How
		}
		return p.HubPubkey, "config"
	}
	if r.Hub == hub && r.Pubkey != "" {
		return r.Pubkey, r.How
	}
	return "", ""
}
func (a *app) ensurePin(name string, p Profile) {
	if a.inHook || a.inMCP {
		return
	}
	if pub, _ := a.effectivePin(name, p); pub != "" {
		return
	}
	hub, err := hubid.NormalizeURL(p.Hub)
	if err != nil {
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	worker := *a
	worker.ctx = ctx
	// Keep the lock while fetching: concurrent commands get one TOFU attempt.
	_ = worker.updatePins(func(records map[string]pinRecord) error {
		r := records[name]
		if r.Hub == hub {
			if r.Pubkey != "" {
				return nil
			}
			if tried, e := time.Parse(time.RFC3339Nano, r.TofuTriedAt); e == nil && time.Since(tried) < 24*time.Hour {
				return nil
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		doc, _, e := verifyHub(ctx, hub, "", p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1")
		if e != nil {
			records[name] = pinRecord{Hub: hub, TofuTriedAt: now}
			return nil
		}
		records[name] = pinRecord{Hub: hub, Pubkey: doc.Pubkey, PinnedAt: now, How: "tofu"}
		return nil
	})
}

type relocationError struct{ Code, Reason string }

func (e *relocationError) Error() string { return e.Code + ": " + e.Reason }

// verifyHub uses only public identity data. No redirects or bearer credentials
// can escape to a candidate destination. An empty pin performs first-use proof.
func verifyHub(ctx context.Context, rawURL, pin string, allowInsecure bool) (doc envelope.HubIdentity, rotated bool, err error) {
	refuse := func(reason string) (envelope.HubIdentity, bool, error) {
		return envelope.HubIdentity{}, false, &relocationError{Code: "relocation_unverified", Reason: reason}
	}
	if err = checkHubURL(rawURL, allowInsecure); err != nil {
		return refuse("hub URL is not allowed")
	}
	normalized, err := hubid.NormalizeURL(rawURL)
	if err != nil {
		return refuse("invalid hub URL")
	}
	challenge := make([]byte, 32)
	if _, err = rand.Read(challenge); err != nil {
		return refuse("cannot generate identity challenge")
	}
	encoded := base64.RawURLEncoding.EncodeToString(challenge)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized+"/v1/hub/identity?challenge="+url.QueryEscape(encoded), nil)
	if err != nil {
		return refuse("cannot create identity request")
	}
	hc := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return refuse("hub identity unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return refuse("hub did not serve an identity")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return refuse("invalid hub identity response")
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return refuse("invalid hub identity response")
	}
	publicURL, e := hubid.NormalizeURL(doc.PublicURL)
	if e != nil || publicURL != normalized {
		return refuse("hub public URL does not match destination")
	}
	if err = hubid.VerifyChallenge(doc.Pubkey, doc.PublicURL, encoded, doc.ChallengeSig); err != nil {
		return refuse("invalid hub identity challenge proof")
	}
	if pin == "" || doc.Pubkey == pin {
		return doc, false, nil
	}
	if doc.Previous != nil && doc.Previous.Pubkey == pin && hubid.VerifyEndorsement(*doc.Previous, doc.Pubkey) == nil {
		return doc, true, nil
	}
	return refuse("hub key does not match the pin")
}
