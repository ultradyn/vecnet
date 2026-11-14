package localruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerEnvironmentHelper(t *testing.T) {
	if filepath.Base(os.Getenv("HOME")) != "runner-fixture-home" {
		return
	}
	values := map[string]string{}
	for _, k := range []string{"HOME", "PATH", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "VECNET_LOCAL_MAINTENANCE", "VECNET_TEST_ENV_LEAK"} {
		values[k] = os.Getenv(k)
	}
	b, _ := json.Marshal(values)
	os.Stdout.Write(b)
	os.Exit(0)
}
func TestRunnerUsesOnlyInjectedEnvironment(t *testing.T) {
	t.Setenv("VECNET_TEST_ENV_LEAK", "secret-sentinel")
	home := filepath.Join(t.TempDir(), "runner-fixture-home")
	o := Options{Home: home, Getenv: func(k string) string {
		switch k {
		case "PATH":
			return "/injected/bin"
		case "XDG_RUNTIME_DIR":
			return "/injected/runtime"
		case "DBUS_SESSION_BUS_ADDRESS":
			return "unix:path=/injected/bus"
		}
		return ""
	}}
	out, e := runner(o)(context.Background(), os.Args[0], "-test.run=^TestRunnerEnvironmentHelper$")
	if e != nil {
		t.Fatal(e)
	}
	var values map[string]string
	if e = json.Unmarshal(out, &values); e != nil {
		t.Fatal(e)
	}
	if values["HOME"] != home || values["PATH"] != "/injected/bin" || values["XDG_RUNTIME_DIR"] != "/injected/runtime" || values["DBUS_SESSION_BUS_ADDRESS"] != "unix:path=/injected/bus" || values["VECNET_LOCAL_MAINTENANCE"] != "0" {
		t.Fatalf("injected environment ignored: %s", out)
	}
	if values["VECNET_TEST_ENV_LEAK"] != "" || strings.Contains(string(out), "secret-sentinel") {
		t.Fatal("runner inherited the real environment")
	}
}
