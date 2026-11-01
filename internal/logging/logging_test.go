package logging_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/logging"
)

func TestRedact(t *testing.T) {
	for _, secret := range []string{
		"vn_peer_ABCDEFGHIJKL", "vn_operator_abc-def_0123", "vn_enrol_ABCDEFGH", "vat_abcdefgh123", "vrt_abcdefgh123",
		"vac_abcdefgh123", "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "crsr_0123456789", "rt_abc.def-123",
	} {
		if got := logging.Redact("token=" + secret + " end"); strings.Contains(got, secret) || !strings.HasSuffix(got, " end") {
			t.Errorf("Redact(%s) = %q", secret, got)
		}
	}
	for _, safe := range []string{"peer_01M6APV0Q703B89334NKT38KDY", "sub_01M6APV0Q703", "start_of_things", "port_8080", "whsec_", "vn_peer_x"} {
		if got := logging.Redact(safe); got != safe {
			t.Errorf("Redact(%s) = %q, want it unchanged", safe, got)
		}
	}
}

func TestHandlerRedactsMessageAndAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(logging.NewHandler(&buf, slog.LevelInfo))
	log.Info("saw vn_peer_ABCDEFGHIJKL", "err", errors.New("bad vat_abcdefgh123"), "key", "crsr_0123456789", "n", 3)
	log.With("group", "x").WithGroup("g").Info("nested", "tok", "vrt_abcdefgh123")
	out := buf.String()
	for _, secret := range []string{"ABCDEFGHIJKL", "abcdefgh123", "0123456789"} {
		if strings.Contains(out, secret) {
			t.Errorf("log kept %s:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "n=3") {
		t.Errorf("a plain attribute was lost:\n%s", out)
	}
}
