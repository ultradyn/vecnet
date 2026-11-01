package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestInitPrintsSecretOnce(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer
	if err := runInit(dir, "Max", &first); err != nil {
		t.Fatalf("first runInit: %v", err)
	}
	if !strings.Contains(first.String(), "vn_operator_") {
		t.Fatalf("first output lacks the operator secret:\n%s", first.String())
	}
	peer := regexp.MustCompile(`peer_[0-9A-HJKMNP-TV-Z]{26}`).FindString(first.String())
	if peer == "" {
		t.Fatalf("first output lacks the operator peer id:\n%s", first.String())
	}
	if err := runInit(dir, "Max", &second); err != nil {
		t.Fatalf("second runInit: %v", err)
	}
	if !strings.Contains(second.String(), "already initialised") {
		t.Fatalf("second output lacks \"already initialised\":\n%s", second.String())
	}
	if strings.Contains(second.String(), "vn_operator_") {
		t.Fatalf("second output repeats a secret:\n%s", second.String())
	}
	if !strings.Contains(second.String(), peer) {
		t.Fatalf("second output does not name the existing peer %s:\n%s", peer, second.String())
	}
}
