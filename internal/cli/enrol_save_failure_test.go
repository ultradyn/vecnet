package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/cli"
)

// TestEnrolSaveFailureNeverPrintsSecret: when the profile cannot be saved
// after the hub issued the credential, the failure names the peer and the
// recovery, never the secret itself (PROTOCOL §12 Logging, §14 inv. 6).
func TestEnrolSaveFailureNeverPrintsSecret(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only config directory")
	}
	const secret = "vn_peer_SYNTHETICSECRETDONOTPRINT0123456789"
	const peer = "peer_00000000000000000000000042"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/enrol" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"peer_id": peer, "secret": secret, "name": "scout"})
	}))
	defer srv.Close()
	u := newUser(t)
	dir := filepath.Dir(u.conf)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// enrol takes the config lock before contacting the hub; pre-create the
	// lock file so only the save after the credential is issued fails.
	if err := os.WriteFile(u.conf+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	var out, errb bytes.Buffer
	code := cli.Run([]string{"enrol", "--hub", srv.URL, "--token", "vn_enrol_Zm9vYmFyYmF6cXV4MDEyMzQ1Njc4OWFiY2RlZmdoaWo", "--profile", "scout", "--allow-insecure-http"},
		strings.NewReader(""), &out, &errb, u.getenv)
	if code == 0 {
		t.Fatalf("enrol into a read-only config dir succeeded: stdout %q stderr %q", out.String(), errb.String())
	}
	for _, s := range []string{out.String(), errb.String()} {
		if strings.Contains(s, secret) || strings.Contains(s, "SYNTHETICSECRET") {
			t.Fatalf("the issued secret reached the output: stdout %q stderr %q", out.String(), errb.String())
		}
	}
	if !strings.Contains(errb.String(), peer) || !strings.Contains(errb.String(), "vecnet peer rotate "+peer) {
		t.Fatalf("stderr %q does not name the peer and the rotate recovery", errb.String())
	}
}
