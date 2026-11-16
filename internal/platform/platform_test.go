package platform

import (
	"runtime"
	"testing"
)

func TestHomePrecedence(t *testing.T) {
	env := map[string]string{"HOME": "explicit", "USERPROFILE": "native"}
	get := func(k string) string { return env[k] }
	if Home(get) != "explicit" {
		t.Fatal("explicit HOME ignored")
	}
	delete(env, "HOME")
	want := ""
	if runtime.GOOS == "windows" {
		want = "native"
	}
	if Home(get) != want {
		t.Fatal("native fallback differs")
	}
	delete(env, "USERPROFILE")
	if Home(get) != "" {
		t.Fatal("missing environment used real user home")
	}
}
