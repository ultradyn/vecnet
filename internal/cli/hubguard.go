package cli

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/XertroV/vecnet/internal/hubid"
)

// insecureEnv is the escape hatch for plain http:// hubs off the LAN.
const insecureEnv = "VECNET_ALLOW_INSECURE_HTTP"

// checkHubURL refuses a hub URL that hubid.CheckHubURL refuses. allow bypasses
// only the plain-http-on-a-public-host rule (enrol --allow-insecure-http or
// VECNET_ALLOW_INSECURE_HTTP). Userinfo, a query and a fragment stay refused.
// The error names the rule and no part of raw.
func checkHubURL(raw string, allow bool) error {
	err := hubid.CheckHubURL(raw)
	if err == nil || (allow && errors.Is(err, hubid.ErrPlainHTTP)) {
		return nil
	}
	return usageErr(err.Error())
}

// useStoredHub refuses a profile whose hub field fails checkHubURL. The
// message tells the operator to edit the config and names no part of the URL.
// Other profiles are left alone.
func (a *app) useStoredHub(name string, p Profile) error {
	err := checkHubURL(p.Hub, p.AllowInsecureHTTP || a.getenv(insecureEnv) == "1")
	if err == nil {
		return nil
	}
	return usageErr(fmt.Sprintf("profile %q is unusable; edit the config: %s", name, err.Error()))
}

// printableHub is the normalised hub URL when checkHubURL accepts raw.
// Anything else is not printed.
func printableHub(raw string, allow bool) (string, bool) {
	if checkHubURL(raw, allow) != nil {
		return "", false
	}
	n, err := hubid.NormalizeURL(raw)
	if err != nil || n == "" {
		return "", false
	}
	return n, true
}

// noticeIDSafe is the canonical notice id. Anything else can carry a secret
// and is not an identifier a diagnostic may print.
var noticeIDSafe = regexp.MustCompile(`^ntc_[0-9A-HJKMNP-TV-Z]{26}$`)

// relocationLine is a moving-notice refusal. raw is printed only after it
// passes checkHubURL. A URL that has not is omitted; a canonical notice id
// is the only other identifier. reason is a fixed phrase, never a raw URL.
func relocationLine(noticeID, raw, reason string, allow bool) string {
	if where, ok := printableHub(raw, allow); ok {
		return "not following " + where + ": " + reason
	}
	if noticeIDSafe.MatchString(noticeID) {
		return "not following notice " + noticeID + ": " + reason
	}
	return "not following: " + reason
}
