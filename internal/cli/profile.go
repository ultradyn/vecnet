package cli

import (
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/hubid"
)

// Profile commands share the ordinary --net/environment/bare operator rules,
// but resolve locally: repoint must never probe the old hub or TOFU a new one
// before the caller has explicitly accepted its identity.
func (a *app) profileSelection() (path, name string, p Profile, err error) {
	path, err = configPath(a.getenv)
	if err != nil {
		return
	}
	cfg, e := loadConfig(path)
	if e != nil {
		err = e
		return
	}
	if err = cfg.checkDefault(); err != nil {
		return
	}
	name = a.namedProfile()
	if name != "" {
		name, p, err = cfg.profile(name)
	} else {
		name, p, _, err = a.resolveBare(cfg)
	}
	return
}
func (a *app) cmdProfile(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: vecnet profile show|pin|repoint URL")
	}
	var flags map[string]flagKind
	switch args[0] {
	case "show":
		flags = map[string]flagKind{}
	case "pin":
		flags = map[string]flagKind{"accept": strFlag}
	case "repoint":
		flags = map[string]flagKind{"force": boolFlag}
	default:
		return usageErr("usage: vecnet profile show|pin|repoint URL")
	}
	o, err := parseFlags("profile "+args[0], args[1:], flags)
	if err != nil {
		return err
	}
	want := 0
	if args[0] == "repoint" {
		want = 1
	}
	if len(o.pos) != want {
		return usageErr("usage: vecnet profile show|pin [--accept FP]|repoint URL [--force]")
	}
	path, name, p, err := a.profileSelection()
	if err != nil {
		return err
	}
	pin, how := a.effectivePin(name, p)
	if args[0] == "show" {
		fp := ""
		if pin != "" {
			fp, err = hubid.Fingerprint(pin)
			if err != nil {
				return err
			}
		}
		var lastMove *moveRecord
		if pp, e := pinsPath(a.getenv); e == nil {
			if records, e := readPins(pp); e == nil {
				if hub, e := hubid.NormalizeURL(p.Hub); e == nil && records[name].Hub == hub {
					lastMove = records[name].LastMove
				}
			}
		}
		if a.json {
			return a.emitValue(struct {
				Profile     string      `json:"profile"`
				Hub         string      `json:"hub"`
				Fingerprint string      `json:"fingerprint"`
				How         string      `json:"how"`
				LastMove    *moveRecord `json:"last_move"`
			}{name, p.Hub, fp, how, lastMove})
		}
		fmt.Fprintf(a.stdout, "profile %s\nhub %s\n", flatten(name), flatten(p.Hub))
		if pin == "" {
			fmt.Fprintln(a.stdout, "pin none")
		} else {
			fmt.Fprintf(a.stdout, "pin %s (%s)\n", fp, flatten(how))
		}
		if lastMove != nil {
			fmt.Fprintf(a.stdout, "last move %s -> %s (%s, %s)\n", flatten(lastMove.From), flatten(lastMove.To), flatten(lastMove.NoticeID), flatten(lastMove.IssuedAt))
		}
		return nil
	}
	allow := p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1"
	if args[0] == "repoint" && o.has("force") {
		if err = checkHubURL(o.pos[0], allow); err != nil {
			return err
		}
		hub, e := hubid.NormalizeURL(o.pos[0])
		if e != nil {
			return e
		}
		if err = a.editProfileSnapshot(path, name, p, pin, map[string]string{"hub": hub, "hub_pubkey": ""}, func(records map[string]pinRecord) { delete(records, name) }); err != nil {
			return err
		}
		return a.ok("profile repointed; identity will be pinned on the next command")
	}
	hub := p.Hub
	if args[0] == "repoint" {
		hub = o.pos[0]
		if pin == "" {
			return &relocationError{Code: "relocation_unverified", Reason: "profile has no pin; run profile pin first or use --force"}
		}
	}
	verifyPin := pin
	if args[0] == "pin" {
		verifyPin = ""
	}
	doc, rotated, err := verifyHub(a.ctx, hub, verifyPin, allow)
	if err != nil {
		return err
	}
	if args[0] == "pin" {
		fp, e := hubid.Fingerprint(doc.Pubkey)
		if e != nil {
			return e
		}
		if o.has("accept") && o.str("accept") != fp {
			return &relocationError{Code: "hub_identity_changed", Reason: "--accept does not match the served fingerprint"}
		}
		if pin != "" && pin != doc.Pubkey && !o.has("accept") {
			return &relocationError{Code: "hub_identity_changed", Reason: "hub key changed; confirm the fingerprint out of band, then use profile pin --accept " + fp}
		}
		how = "accept"
	} else if rotated {
		how = "rotation"
	} else if how == "" || how == "config" {
		how = "accept"
	}
	edits := map[string]string{"hub_pubkey": doc.Pubkey}
	if args[0] == "repoint" {
		edits["hub"], err = hubid.NormalizeURL(hub)
		if err != nil {
			return err
		}
	}
	normalized, _ := hubid.NormalizeURL(hub)
	if err = a.editProfileSnapshot(path, name, p, pin, edits, func(records map[string]pinRecord) {
		r := records[name]
		if r.Hub != normalized {
			r = pinRecord{}
		}
		r.Hub, r.Pubkey, r.How = normalized, doc.Pubkey, how
		r.PinnedAt = time.Now().UTC().Format(time.RFC3339Nano)
		r.TofuTriedAt = ""
		records[name] = r
	}); err != nil {
		return err
	}
	return a.ok("profile identity saved")
}

// The pins lock encloses the conditional config edit, so neither enrol nor a
// concurrent sidecar update can substitute a different trust snapshot. Keep
// the pin update in the same critical section as its snapshot check.
func (a *app) editProfileSnapshot(path, name string, verified Profile, pin string, edits map[string]string, update func(map[string]pinRecord)) error {
	return a.updatePins(func(records map[string]pinRecord) error {
		if err := editProfilesIf(path, map[string]map[string]string{name: edits}, func(current Config) error {
			p, exists := current.Profiles[name]
			currentPin, _ := a.effectivePin(name, p)
			if !exists || p != verified || currentPin != pin {
				return fmt.Errorf("profile edit: profile changed while verifying identity; retry the command")
			}
			return nil
		}); err != nil {
			return err
		}
		update(records)
		return nil
	})
}
