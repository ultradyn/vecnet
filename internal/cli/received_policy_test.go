package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReceivedNoticePolicy(t *testing.T) {
	constructors := []string{"connect", "profile-follow", "codex-auto", "codex-bind", "launcher", "enrol"}
	policies := []struct {
		name                       string
		profile, environment, flag bool
	}{
		{"default", false, false, false}, {"profile-only", true, false, false},
		{"environment-only", false, true, false}, {"both", true, true, false},
	}
	targets := []struct {
		name, raw string
		public    bool
	}{
		{"public-http", "http://public.example.invalid/Prefix/%2F", true},
		{"loopback-http", "http://127.0.0.1:8080/Prefix", false},
		{"lan-http", "http://hub.lan:8080/Prefix", false},
		{"https-ipv6", "https://[2001:db8::1]:8443/Prefix/%2F", false},
	}
	for _, kind := range constructors {
		for _, source := range []string{"https://source.example.invalid", "http://source.lan"} {
			for _, policy := range policies {
				for _, target := range targets {
					t.Run(kind+"/"+source+"/"+policy.name+"/"+target.name, func(t *testing.T) {
						f := newReceivedFixture(t, source)
						f.profile(policy.profile)
						if policy.environment {
							f.env[insecureEnv] = "1"
						}
						var launcher []string
						if kind == "launcher" {
							launcher = f.launcherArgs()
						}
						n := f.notice()
						n.NewURL = target.raw
						if err := f.key.SignNotice(&n); err != nil {
							t.Fatal(err)
						}
						details := map[string]any{"new_url": target.raw, "notice": n}
						f.reply(410, "hub_moved", "hub moved", details)
						before := f.snapshot()
						original := receivedJSON(t, n)
						allowed := policy.profile || policy.environment
						if kind == "enrol" {
							allowed = policy.environment || policy.flag
						}
						message, want := "hub moved", any(details)
						if target.public && !allowed {
							message, want = "invalid hub relocation", nil
						}
						he := receivedError(t, f.constructor(kind, policy.flag, launcher), 410, "hub_moved", message, want)
						if kind == "enrol" {
							hint := ""
							if message == "hub moved" {
								hint = "hub moved to " + target.raw + "; ask for an invite there"
							}
							if he.Hint != hint {
								t.Errorf("enrol hint=%q want=%q", he.Hint, hint)
							}
						}
						if !bytes.Equal(original, receivedJSON(t, n)) {
							t.Error("signed input changed")
						}
						f.unchanged(before)
						sourceCalls, destination, closed, read := f.counts()
						if sourceCalls != 1 || destination != 0 || closed != 1 || read != len(f.body) {
							t.Errorf("constructor source/destination/closed/read=%d/%d/%d/%d", sourceCalls, destination, closed, read)
						}
					})
				}
			}
		}
	}
	t.Run("nested-public-http-policy", func(t *testing.T) {
		// Independently admit/refuse each nested URL even when the top target is
		// safe. URL projection must not introduce source-match/authentication rules.
		for _, kind := range constructors {
			for _, field := range []string{"hub", "new_url"} {
				for _, policy := range policies[:3] {
					t.Run(kind+"/"+field+"/"+policy.name, func(t *testing.T) {
						f := newReceivedFixture(t, "https://source.example.invalid")
						f.profile(policy.profile)
						if policy.environment {
							f.env[insecureEnv] = "1"
						}
						var launcher []string
						if kind == "launcher" {
							launcher = f.launcherArgs()
						}
						n := f.notice()
						if field == "hub" {
							n.Hub = "http://public.example.invalid"
						} else {
							n.NewURL = "http://public.example.invalid"
						}
						if err := f.key.SignNotice(&n); err != nil {
							t.Fatal(err)
						}
						details := map[string]any{"new_url": receivedDestination, "notice": n}
						f.reply(410, "hub_moved", "hub moved", details)
						before := f.snapshot()
						allowed := policy.profile || policy.environment
						if kind == "enrol" {
							allowed = policy.environment
						}
						message, want := "invalid hub relocation", any(nil)
						if allowed {
							message, want = "hub moved", details
						}
						receivedError(t, f.constructor(kind, false, launcher), 410, "hub_moved", message, want)
						f.unchanged(before)
					})
				}
			}
		}
	})
	t.Run("opt-in-does-not-bypass-url-structure", func(t *testing.T) {
		for _, kind := range constructors {
			for _, field := range []string{"top", "hub", "new_url"} {
				for _, bad := range []struct{ name, raw string }{
					{"password", "https://user:" + receivedCanary + "@example.invalid"},
					{"username", "https://" + receivedCanary + "@example.invalid"},
					{"query", "https://example.invalid/?token=" + receivedCanary},
					{"fragment", "https://example.invalid/#" + receivedCanary},
					{"bare-query", "https://example.invalid/?"}, {"bare-fragment", "https://example.invalid/#"},
					{"authority", "https://"}, {"ipv6", "https://[::1"}, {"escape", "https://example.invalid/%zz"},
				} {
					t.Run(kind+"/"+field+"/"+bad.name, func(t *testing.T) {
						f := newReceivedFixture(t, "https://source.example.invalid")
						f.profile(true)
						f.env[insecureEnv] = "1"
						var launcher []string
						if kind == "launcher" {
							launcher = f.launcherArgs()
						}
						n := map[string]any{"hub": f.hub, "new_url": receivedDestination}
						details := map[string]any{"new_url": receivedDestination, "notice": n}
						if field == "top" {
							details["new_url"] = bad.raw
						} else {
							n[field] = bad.raw
						}
						f.reply(410, "hub_moved", "hub moved", details)
						before := f.snapshot()
						receivedError(t, f.constructor(kind, true, launcher), 410, "hub_moved", "invalid hub relocation", nil)
						f.unchanged(before)
					})
				}
			}
		}
	})
	t.Run("enrol-flag-and-persistence", receivedEnrolPolicy)
	t.Run("watch-refresh-and-adopt", receivedWatchPolicy)
	t.Run("signed-public-http-follow", receivedPublicFollow)
}

func receivedEnrolPolicy(t *testing.T) {
	for _, tc := range []struct {
		name              string
		flag, environment bool
	}{
		{"flag-only", true, false}, {"environment-only", false, true}, {"both", true, true},
	} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, jsonMode), func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				if tc.environment {
					f.env[insecureEnv] = "1"
				}
				target := "http://public.example.invalid/Prefix/%2F"
				f.reply(410, "hub_moved", receivedCanary, map[string]any{"new_url": target})
				before := f.snapshot()
				args := []string{"enrol", "--hub", f.hub, "--token", "fixture-token", "--profile", "new-profile"}
				if tc.flag {
					args = append(args, "--allow-insecure-http")
				}
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				code, out, errs := f.run(args...)
				want := "hub_moved: hub moved\nhub moved to " + target + "; ask for an invite there\n"
				if code != 2 || out != "" || errs != want {
					t.Errorf("enrol exit=%d stdout=%q stderr=%q want=%q", code, out, errs, want)
				}
				f.unchanged(before)
				f.mu.Lock()
				f.status, f.body = 200, receivedJSON(t, map[string]string{"peer_id": "peer_fixture", "secret": "vn_peer_NEW_PRIVATE", "name": "fixture", "hub_pubkey": f.key.Pubkey()})
				f.mu.Unlock()
				code, out, errs = f.run(args...)
				if code != 0 || strings.Contains(out+errs, "vn_peer_NEW_PRIVATE") {
					t.Fatalf("successful enrol exit=%d stdout=%s stderr=%s", code, out, errs)
				}
				cfg, err := loadConfig(f.env["VECNET_CONFIG"])
				if err != nil || cfg.Profiles["new-profile"].Hub != f.hub || cfg.Profiles["new-profile"].AllowInsecureHTTP != tc.flag {
					t.Errorf("enrol must persist flag only: %+v %v", cfg, err)
				}
				source, destination, closed, _ := f.counts()
				if source != 2 || destination != 0 || closed != 2 {
					t.Errorf("enrol source/destination/closed=%d/%d/%d", source, destination, closed)
				}
			})
		}
	}
}

func receivedWatchPolicy(t *testing.T) {
	for _, adopt := range []bool{false, true} {
		for _, tc := range []struct {
			name                                   string
			initial, next, initialEnv, environment bool
		}{
			{"enable-profile", false, true, false, false}, {"revoke-profile", true, false, false, false},
			{"enable-environment", false, false, false, true}, {"revoke-environment", false, false, true, false},
			{"default", false, false, false, false},
		} {
			t.Run(fmt.Sprintf("%s/adopt=%t", tc.name, adopt), func(t *testing.T) {
				f := newReceivedFixture(t, "https://source.example.invalid")
				f.profile(tc.initial)
				if tc.initialEnv {
					f.env[insecureEnv] = "1"
				}
				a := f.app()
				c, _, _, err := a.connect()
				if err != nil {
					t.Fatal(err)
				}
				w := &watcher{a: a, c: c, name: "p", pin: f.key.Pubkey()}
				if adopt {
					old := f.hub
					f.hub = "https://adopted.example.invalid"
					f.allow[f.hub] = true
					f.profile(tc.next)
					if err := a.updatePins(func(records map[string]pinRecord) error {
						records["p"] = pinRecord{Hub: f.hub, Pubkey: f.key.Pubkey(), How: "follow", LastMove: &moveRecord{From: old, To: f.hub, NoticeID: "ntc_fixture"}}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					f.profile(tc.next)
				}
				if tc.environment {
					f.env[insecureEnv] = "1"
				} else {
					delete(f.env, insecureEnv)
				}
				before := f.snapshot()
				if adopt && !w.adoptFollowed(nil) {
					t.Fatal("fixture's recorded follow was not adopted")
				}
				if err := w.verifyConnection(); err != nil {
					t.Fatalf("identity verification: %v", err)
				}
				if c.base != f.hub {
					t.Fatalf("watch still connected to %q", c.base)
				}
				target := "http://public.example.invalid/Next/%2F"
				f.reply(410, "hub_moved", "hub moved", map[string]any{"new_url": target})
				message, details := "invalid hub relocation", any(nil)
				if tc.next || tc.environment {
					message, details = "hub moved", map[string]any{"new_url": target}
				}
				req, err := c.newRequest(a.ctx, "GET", "/v1/events", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept", "text/event-stream")
				_, err = c.send(req)
				receivedError(t, err, 410, "hub_moved", message, details)
				f.unchanged(before)
				source, destination, closed, _ := f.counts()
				if source != 1 || destination != 0 || closed != 1 {
					t.Errorf("watch source/destination/closed=%d/%d/%d", source, destination, closed)
				}
			})
		}
	}
}

func receivedPublicFollow(t *testing.T) {
	for _, envOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("environment-only=%t", envOnly), func(t *testing.T) {
			f := newReceivedFixture(t, "https://source.example.invalid")
			f.profile(!envOnly)
			if envOnly {
				f.env[insecureEnv] = "1"
			}
			n := f.notice()
			// Use an origin target for the actual signed-follow HTTP challenge.
			n.NewURL = "http://public.example.invalid"
			f.allow[n.NewURL] = true
			if err := f.key.SignNotice(&n); err != nil {
				t.Fatal(err)
			}
			f.reply(410, "hub_moved", "hub moved", map[string]any{"new_url": n.NewURL, "notice": n})
			original := receivedJSON(t, n)
			code, out, errs := f.run("--net", "p", "peers")
			if code != 0 || !strings.Contains(out, "peer_fixture") || !strings.Contains(errs, "verified") || !bytes.Equal(original, receivedJSON(t, n)) {
				t.Fatalf("opted-in follow: %d stdout=%s stderr=%s", code, out, errs)
			}
			cfg, err := loadConfig(f.env["VECNET_CONFIG"])
			if err != nil || cfg.Profiles["p"].Hub != n.NewURL || cfg.Profiles["p"].AllowInsecureHTTP != !envOnly {
				t.Errorf("opted-in followed profile=%+v err=%v", cfg, err)
			}
			source, destination, closed, _ := f.counts()
			if source != 1 || destination != 2 || closed != 1 {
				t.Errorf("follow source/destination/closed=%d/%d/%d", source, destination, closed)
			}
		})
	}
}
