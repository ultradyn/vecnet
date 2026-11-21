package cli

import "net/http"

// cmdWhoami reports the credential's identity from the hub. Neither cached
// profile metadata nor --as identifies the authenticated caller.
func (a *app) cmdWhoami(args []string) error {
	o, err := parseFlags("whoami", args, nil)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 {
		return usageErr("usage: vecnet whoami [--json]")
	}
	var me map[string]any
	if err := a.call(http.MethodGet, "/v1/peers/me", nil, &me); err != nil {
		return err
	}
	return a.emitObject(me, "kind", "peer_id", "principal_id")
}
