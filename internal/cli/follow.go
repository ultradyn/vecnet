package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

func movedNotice(err error) (envelope.HubNotice, bool) {
	var he *hubError
	if !errors.As(err, &he) || he.Status != http.StatusGone || he.Code != "hub_moved" {
		return envelope.HubNotice{}, false
	}
	raw, e := json.Marshal(he.Details["notice"])
	if e != nil {
		return envelope.HubNotice{}, false
	}
	var n envelope.HubNotice
	if json.Unmarshal(raw, &n) != nil || n.Kind != "moving" || n.NoticeID == "" {
		return envelope.HubNotice{}, false
	}
	return n, true
}

func (a *app) follow(ctx context.Context, name string, p Profile, n envelope.HubNotice) (string, error) {
	refuse := func(reason string) (string, error) {
		return "", &relocationError{Code: "relocation_unverified", Reason: reason}
	}
	allow := p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1"
	// Refuse a secret-bearing URL before any request, and before printing it.
	if checkHubURL(p.Hub, allow) != nil {
		return refuse("invalid source hub")
	}
	if checkHubURL(n.Hub, allow) != nil || n.Kind != "moving" {
		return refuse("notice does not describe this hub moving")
	}
	if checkHubURL(n.NewURL, allow) != nil {
		return refuse("invalid or unchanged destination")
	}
	old, err := hubid.NormalizeURL(p.Hub)
	if err != nil {
		return refuse("invalid source hub")
	}
	source, err := hubid.NormalizeURL(n.Hub)
	if err != nil || source != old || n.Kind != "moving" {
		return refuse("notice does not describe this hub moving")
	}
	next, err := hubid.NormalizeURL(n.NewURL)
	if err != nil || next == old {
		return refuse("invalid or unchanged destination")
	}
	pin, _ := a.effectivePin(name, p)
	snapshotPin := pin
	if pin == "" {
		doc, _, e := verifyHub(ctx, old, "", allow)
		if e != nil {
			return refuse("cannot pin the old hub")
		}
		pin = doc.Pubkey
	}
	if hubid.VerifyNotice(pin, n) != nil {
		return refuse("invalid moving notice signature")
	}
	issued, err := envelope.ParseTime(n.IssuedAt)
	if err != nil {
		return refuse("invalid notice issue time")
	}
	expires, err := envelope.ParseTime(n.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || n.ClearedAt != "" {
		return refuse("moving notice is no longer active")
	}
	pp, err := pinsPath(a.getenv)
	if err != nil {
		return refuse("cannot locate pins")
	}
	records, err := readPins(pp)
	if err != nil {
		return refuse("cannot read pins")
	}
	replay := func(r pinRecord) bool {
		if r.LastMove == nil {
			return false
		}
		last, e := envelope.ParseTime(r.LastMove.IssuedAt)
		return e != nil || !issued.After(last)
	}
	if replay(records[name]) {
		return refuse("moving notice was already followed")
	}
	_, _, err = verifyHub(ctx, next, pin, allow)
	if err != nil {
		return "", err
	}
	path, err := configPath(a.getenv)
	if err != nil {
		return refuse("cannot locate config")
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return refuse("cannot read config")
	}
	edits := map[string]map[string]string{}
	snapshots := map[string]Profile{}
	for profile, candidate := range cfg.Profiles {
		hub, e := hubid.NormalizeURL(candidate.Hub)
		candidatePin, _ := a.effectivePin(profile, candidate)
		if e == nil && hub == old && (candidatePin == pin || profile == name && candidatePin == snapshotPin) {
			edits[profile] = map[string]string{"hub": next, "hub_pubkey": pin}
			snapshots[profile] = candidate
		}
	}
	if current, ok := snapshots[name]; !ok || current != p {
		return refuse("profile changed while verifying; retry the command")
	}
	err = a.updatePins(func(current map[string]pinRecord) error {
		if replay(current[name]) {
			return fmt.Errorf("moving notice was already followed")
		}
		if !reflect.DeepEqual(records, current) {
			return fmt.Errorf("pins changed while verifying; retry the command")
		}
		if err := editProfilesIf(path, edits, func(latest Config) error {
			if !reflect.DeepEqual(cfg, latest) {
				return fmt.Errorf("profiles changed while verifying; retry the command")
			}
			return nil
		}); err != nil {
			return err
		}
		for profile := range edits {
			r := current[profile]
			// Every rewritten profile records the move, whatever pinned the
			// old hub (§21.5 The pin).
			r.Hub, r.Pubkey, r.How = next, pin, "follow"
			r.TofuTriedAt = ""
			if r.PinnedAt == "" {
				r.PinnedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			r.LastMove = &moveRecord{From: old, To: next, NoticeID: n.NoticeID, IssuedAt: n.IssuedAt}
			current[profile] = r
		}
		return nil
	})
	if err != nil {
		return refuse(err.Error())
	}
	fp, _ := hubid.Fingerprint(pin)
	where := "the hub"
	if shown, ok := printableHub(next, allow); ok {
		where = shown
	}
	fmt.Fprintf(a.stderr, "%s\n", unbidi(flatten(fmt.Sprintf("profile %s now uses %s (key %s verified)", name, where, fp))))
	return next, nil
}

func (a *app) followFailure(err error) (bool, error) {
	var he *hubError
	if !errors.As(err, &he) || he.Status != http.StatusGone || he.Code != "hub_moved" {
		return false, err
	}
	_, name, p, e := a.profileSelection()
	if e != nil {
		return true, e
	}
	if a.getenv("VECNET_NO_FOLLOW") == "1" {
		return true, &relocationError{Code: "relocation_unverified", Reason: fmt.Sprintf("hub moved; run `vecnet --net %s profile follow`", peerName(name))}
	}
	n, ok := movedNotice(err)
	if !ok {
		return true, &relocationError{Code: "relocation_unverified", Reason: "hub returned an invalid moving notice"}
	}
	_, e = a.follow(a.ctx, name, p, n)
	return true, e
}
