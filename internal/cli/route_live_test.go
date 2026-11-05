package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRouteListAndRemove(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	owner, _ := env.NewPeer("grokbot", "route-owner")
	const key = "crsr_route_list_secret_canary"
	id, err := env.Hub.AddGrokbotRoute(context.Background(), "peer", owner, routineURL, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"route", "list"}, {"route", "list", "--json"}} {
		out := op.mustRun(args...)
		for _, want := range []string{id, owner, "grokbot", "api2.cursor.sh", "200", envelope.FormatTime(env.Now.Now())} {
			if !strings.Contains(out, want) {
				t.Errorf("route list %v = %q, missing %q", args, out, want)
			}
		}
		for _, secret := range []string{key, "123e4567-e89b-12d3-a456-426614174000", "key_enc", "whsec_"} {
			if strings.Contains(out, secret) {
				t.Errorf("route list leaks %q: %s", secret, out)
			}
		}
		if len(args) == 3 {
			var routes []map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &routes); err != nil || len(routes) != 1 {
				t.Fatalf("route list --json = %q, want one-item array: %v", out, err)
			}
			for _, field := range []string{"route_id", "owner_id", "owner_kind", "kind", "host", "verified_at", "runs_per_hour"} {
				if len(routes[0][field]) == 0 || string(routes[0][field]) == "null" {
					t.Errorf("route list --json missing %s: %s", field, out)
				}
			}
		}
	}
	op.mustRun("route", "remove", id)
	if n := routeCount(t, env); n != 0 {
		t.Fatalf("route remove left %d routes", n)
	}
	if code, _, errs := op.run("route", "remove", id); code != 2 || !strings.Contains(errs, "not_found") {
		t.Fatalf("route remove unknown = %d %q, want 2 not_found", code, errs)
	}
	if out := op.mustRun("route", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty route list = %q, want []", out)
	}
}

func TestRouteRotateKeyFile(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	owner, _ := env.NewPeer("grokbot", "route-owner")
	ctx := context.Background()
	const old = "crsr_route_old_secret_canary"
	const next = "crsr_route_next_secret_canary"
	id, err := env.Hub.AddGrokbotRoute(ctx, "peer", owner, routineURL, old)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "routine-key")
	if err := os.WriteFile(path, []byte(next+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"route", "rotate", id, "--key-file", path}, {"--json", "route", "rotate", id, "--key-file", path}} {
		code, out, errs := op.run(args...)
		if code != 0 || errs != "" {
			t.Fatalf("route rotate = %d %q %q, want quiet success", code, out, errs)
		}
		if strings.Contains(out+errs, next) || strings.Contains(out+errs, old) {
			t.Fatalf("route rotate echoes a key: %q %q", out, errs)
		}
		if got, err := env.Hub.GrokbotKey(ctx, id); err != nil || got != next {
			t.Fatalf("rotated key = %q, %v", got, err)
		}
	}
	if code, _, errs := op.run("route", "rotate", id); code != 1 || !strings.Contains(errs, "usage") {
		t.Fatalf("route rotate without file = %d %q, want usage", code, errs)
	}
}

func TestRouteKeyFileRejectsBearerPrefix(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	owner, _ := env.NewPeer("grokbot", "route-owner")
	ctx := context.Background()
	const key = "crsr_route_key_secret_canary"
	id, err := env.Hub.AddGrokbotRoute(ctx, "peer", owner, routineURL, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "routine-key")
	if err := os.WriteFile(path, []byte("Bearer "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"route", "rotate", id, "--key-file", path},
		{"route", "add", "--owner", owner, "--kind", "grokbot", "--url", routineURL, "--key-file", path},
	} {
		code, out, errs := op.run(args...)
		if code != 1 || errs == "" {
			t.Fatalf("Bearer-prefixed file = %d %q %q, want local refusal", code, out, errs)
		}
		if strings.Contains(out+errs, key) {
			t.Fatalf("key-file refusal echoes the key: %q %q", out, errs)
		}
	}
	if n := routeCount(t, env); n != 1 {
		t.Fatalf("refused key created another route: %d", n)
	}
	if got, err := env.Hub.GrokbotKey(ctx, id); err != nil || got != key {
		t.Fatalf("refused key changed existing route: %q, %v", got, err)
	}
}
