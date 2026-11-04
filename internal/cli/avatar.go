package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Avatars (PROTOCOL §8.1): `vecnet avatar set FILE|URL`, `vecnet avatar
// clear`, and the operator's `vecnet peer avatar <peer> FILE|URL|--clear`.

// avatarMaxBytes mirrors the hub's 2 MiB cap, so an oversized file is refused
// before it is read or sent.
const avatarMaxBytes = 2 << 20

const avatarUsage = "usage: vecnet avatar set <FILE|https://URL> | avatar clear"

// avatarBody is the PUT body for arg: {"url"} for an http(s) URL, which the
// hub fetches itself, else the file's bytes with their media type.
func avatarBody(arg string) (any, error) {
	if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
		return map[string]string{"url": arg}, nil
	}
	st, err := os.Stat(arg)
	if err != nil {
		return nil, usageErr("avatar: " + err.Error())
	}
	if !st.Mode().IsRegular() {
		return nil, usageErr("avatar: " + arg + " is not a regular file")
	}
	if st.Size() > avatarMaxBytes {
		return nil, usageErr(fmt.Sprintf("avatar: %s is %d bytes; the cap is 2 MiB (2097152)", arg, st.Size()))
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return nil, usageErr("avatar: " + err.Error())
	}
	return rawBody{r: bytes.NewReader(data), contentType: mediaTypeOf(arg), length: int64(len(data))}, nil
}

// putAvatar sends an avatar PUT and prints {avatar}.
func (a *app) putAvatar(path string, q url.Values, arg string) error {
	body, err := avatarBody(arg)
	if err != nil {
		return err
	}
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, uploadTimeout)
	defer cancel()
	var out map[string]any
	if err := c.doNoTimeout(ctx, http.MethodPut, path, q, body, &out); err != nil {
		return err
	}
	return a.emitObject(out, "avatar")
}

// cmdAvatar sets or clears the caller's own avatar; a principal names its
// peer with --as.
func (a *app) cmdAvatar(args []string) error {
	o, err := parseFlags("avatar", args, map[string]flagKind{})
	if err != nil {
		return err
	}
	var q url.Values
	if a.as != "" {
		q = url.Values{"as": {a.as}}
	}
	switch {
	case len(o.pos) == 2 && o.pos[0] == "set":
		return a.putAvatar("/v1/peers/me/avatar", q, o.pos[1])
	case len(o.pos) == 1 && o.pos[0] == "clear":
		if err := a.callQuery(http.MethodDelete, "/v1/peers/me/avatar", q, nil, nil); err != nil {
			return err
		}
		return a.ok("avatar cleared")
	}
	return usageErr(avatarUsage)
}

// peerAvatar is the operator's `peer avatar <peer> FILE|URL|--clear`.
func (a *app) peerAvatar(peer, arg string, clear bool) error {
	path := "/v1/admin/peers/" + pathEscape(peer) + "/avatar"
	switch {
	case clear && arg == "":
		if err := a.call(http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		return a.ok("avatar cleared for " + peer)
	case !clear && arg != "":
		return a.putAvatar(path, nil, arg)
	}
	return usageErr("usage: vecnet peer avatar <peer> (FILE|https://URL|--clear)")
}
