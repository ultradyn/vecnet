package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyGitHub(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	secret := "s3cret"
	good := sign(secret, body)
	if !VerifyGitHub(secret, good, body) {
		t.Fatal("correct signature rejected")
	}

	flipped := []byte(good)
	if flipped[len(flipped)-1] == '0' {
		flipped[len(flipped)-1] = '1'
	} else {
		flipped[len(flipped)-1] = '0'
	}
	bad := map[string]string{
		"wrong mac":           string(flipped),
		"other secret":        sign("other", body),
		"other body":          sign(secret, []byte("{}")),
		"no sha256= prefix":   strings.TrimPrefix(good, "sha256="),
		"sha1 scheme":         "sha1=" + strings.TrimPrefix(good, "sha256="),
		"uppercase prefix":    "SHA256=" + strings.TrimPrefix(good, "sha256="),
		"not hex":             "sha256=zz" + good[9:],
		"truncated":           good[:len(good)-2],
		"empty":               "",
		"prefix only":         "sha256=",
		"signature plus junk": good + "00",
	}
	for name, sig := range bad {
		if VerifyGitHub(secret, sig, body) {
			t.Errorf("%s: %q accepted", name, sig)
		}
	}

	// Constant-time: no input returns before the HMAC has run, so a malformed
	// header costs what a well-formed wrong one costs.
	runs := 0
	orig := newMAC
	newMAC = func(h func() hash.Hash, key []byte) hash.Hash {
		runs++
		return orig(h, key)
	}
	defer func() { newMAC = orig }()
	for name, sig := range bad {
		runs = 0
		VerifyGitHub(secret, sig, body)
		if runs != 1 {
			t.Errorf("%s: HMAC ran %d times, want 1", name, runs)
		}
	}
	runs = 0
	VerifyGitHub(secret, good, body)
	if runs != 1 {
		t.Errorf("good signature: HMAC ran %d times, want 1", runs)
	}
}

func TestParseGitHubFixtures(t *testing.T) {
	e, err := ParseGitHub("issue_comment", fixture(t, "issue_comment.created.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := GitHubEvent{
		Event: "issue_comment", Action: "created", Repo: "o/r", Number: 196,
		Title: "radio listen does not finalize on 'over'", Actor: "alice",
		Excerpt: "Reproduced again today; attaching the transcript.",
		URL:     "https://github.com/o/r/issues/196#issuecomment-2450117799",
	}
	if e != want {
		t.Fatalf("issue_comment = %+v, want %+v", e, want)
	}
	if got := e.OriginThread(); got != "o/r#196" {
		t.Fatalf("OriginThread = %q", got)
	}
	if got := e.Name(); got != "issue_comment.created" {
		t.Fatalf("Name = %q", got)
	}

	e, err = ParseGitHub("pull_request", fixture(t, "pull_request.opened.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = GitHubEvent{
		Event: "pull_request", Action: "opened", Repo: "o/r", Number: 212,
		Title: "Finalize a radio listen on the word 'over'", Actor: "alice",
		URL: "https://github.com/o/r/pull/212",
	}
	if e != want {
		t.Fatalf("pull_request = %+v, want %+v", e, want)
	}
}

func TestParseGitHubKinds(t *testing.T) {
	const repo = `"repository":{"full_name":"o/r"},"sender":{"login":"bob"}`
	cases := []struct {
		event, body string
		want        GitHubEvent
		origin      string
	}{
		{
			"issues",
			`{"action":"opened","issue":{"number":7,"title":"t","html_url":"https://x/7"},` + repo + `}`,
			GitHubEvent{Event: "issues", Action: "opened", Repo: "o/r", Number: 7, Title: "t", Actor: "bob", URL: "https://x/7"},
			"o/r#7",
		},
		{
			"pull_request_review",
			`{"action":"submitted","review":{"html_url":"https://x/r"},"pull_request":{"number":8,"title":"p","html_url":"https://x/8"},` + repo + `}`,
			GitHubEvent{Event: "pull_request_review", Action: "submitted", Repo: "o/r", Number: 8, Title: "p", Actor: "bob", URL: "https://x/r"},
			"o/r#8",
		},
		{
			"pull_request_review_comment",
			`{"action":"created","comment":{"body":"\r\n first line\r\nsecond","html_url":"https://x/c"},"pull_request":{"number":9,"title":"p","html_url":"https://x/9"},` + repo + `}`,
			GitHubEvent{Event: "pull_request_review_comment", Action: "created", Repo: "o/r", Number: 9, Title: "p", Excerpt: "first line", Actor: "bob", URL: "https://x/c"},
			"o/r#9",
		},
		{
			"push",
			`{"ref":"refs/heads/main","compare":"https://x/compare","head_commit":{"message":"fix it\n\nbody"},` + repo + `}`,
			GitHubEvent{Event: "push", Repo: "o/r", Ref: "refs/heads/main", Title: "fix it", Actor: "bob", URL: "https://x/compare"},
			"o/r@refs/heads/main",
		},
		{
			"release",
			`{"action":"published","release":{"name":"v1","tag_name":"v1.0.0","html_url":"https://x/rel"},` + repo + `}`,
			GitHubEvent{Event: "release", Action: "published", Repo: "o/r", Title: "v1", Actor: "bob", URL: "https://x/rel"},
			"o/r#release",
		},
		{
			"workflow_run",
			`{"action":"completed","workflow_run":{"name":"ci","html_url":"https://x/run"},` + repo + `}`,
			GitHubEvent{Event: "workflow_run", Action: "completed", Repo: "o/r", Title: "ci", Actor: "bob", URL: "https://x/run"},
			"o/r#workflow_run",
		},
		{
			"star",
			`{"action":"created",` + repo + `}`,
			GitHubEvent{Event: "star", Action: "created", Repo: "o/r", Actor: "bob"},
			"o/r#star",
		},
	}
	for _, c := range cases {
		t.Run(c.event, func(t *testing.T) {
			e, err := ParseGitHub(c.event, []byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if e != c.want {
				t.Fatalf("got %+v, want %+v", e, c.want)
			}
			if got := e.OriginThread(); got != c.origin {
				t.Fatalf("OriginThread = %q, want %q", got, c.origin)
			}
		})
	}
	if _, err := ParseGitHub("issues", []byte(`not json`)); err == nil {
		t.Fatal("non-JSON body parsed")
	} else if !errors.Is(err, ErrBadPayload) {
		t.Fatalf("err = %v, want ErrBadPayload", err)
	}
}

func TestSummaryGitHub(t *testing.T) {
	e := GitHubEvent{Event: "issue_comment", Action: "created", Repo: "o/r", Number: 196,
		Title: "a bug", Actor: "alice", URL: "https://x/196"}
	want := `[o/r] issue_comment.created on #196 by alice: "a bug" https://x/196`
	if got := SummaryGitHub(e); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}

	// A comment quotes what was said; the title stays the thread's.
	e.Excerpt = "Reproduced again today."
	want = `[o/r] issue_comment.created on #196 by alice: "Reproduced again today." https://x/196`
	if got := SummaryGitHub(e); got != want {
		t.Fatalf("comment summary = %q, want %q", got, want)
	}

	e = GitHubEvent{Event: "push", Repo: "o/r", Ref: "refs/heads/main", Title: "fix", Actor: "bob", URL: "https://x/c"}
	want = `[o/r] push on refs/heads/main by bob: "fix" https://x/c`
	if got := SummaryGitHub(e); got != want {
		t.Fatalf("push summary = %q, want %q", got, want)
	}

	// The cut lands inside a four-byte rune: it backs off to a rune boundary
	// and never exceeds 4096 bytes.
	e.Title = strings.Repeat("😀", 2000)
	got := SummaryGitHub(e)
	if len(got) > SummaryMax {
		t.Fatalf("summary is %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("summary is not valid UTF-8")
	}
	if len(got) < SummaryMax-utf8.UTFMax {
		t.Fatalf("summary cut too short: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, `" https://x/c`) {
		t.Fatalf("an oversize title cost the URL: ...%q", got[len(got)-40:])
	}
	e.Title, e.Excerpt = "t", strings.Repeat("😀", 2000)
	if got := SummaryGitHub(e); len(got) > SummaryMax || !strings.HasSuffix(got, `" https://x/c`) {
		t.Fatalf("oversize excerpt: %d bytes, ...%q", len(got), got[len(got)-40:])
	}
}

func TestGitHubMessage(t *testing.T) {
	body := fixture(t, "pull_request.opened.json")
	m, err := GitHubMessage("src_x", "d-1", "pull_request", body)
	if err != nil {
		t.Fatal(err)
	}
	if m.SrcID != "src_x" || m.ProviderDeliveryID != "d-1" || m.OriginEventID != "d-1" ||
		m.OriginThreadID != "o/r#212" || m.EventName != "pull_request.opened" ||
		m.Title != "o/r#212: Finalize a radio listen on the word 'over'" ||
		m.MediaType != "application/json" || string(m.Raw) != string(body) {
		t.Fatalf("message = %+v", m)
	}

	// A title at GitHub's own limit does not fit thread_title's 200 characters.
	long := `{"action":"opened","issue":{"number":1,"title":"` + strings.Repeat("é", 256) +
		`"},"repository":{"full_name":"o/r"}}`
	m, err = GitHubMessage("src_x", "d-2", "issues", []byte(long))
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(m.Title); n != threadTitleMax {
		t.Fatalf("title is %d characters, want %d", n, threadTitleMax)
	}

	if _, err := GitHubMessage("src_x", "d-3", "issues", []byte(`[1]`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("array body: err = %v, want ErrBadPayload", err)
	}

	// An over-long ref still fits $defs/origin's thread_id.
	ref := "refs/heads/" + strings.Repeat("x", 300)
	e := GitHubEvent{Event: "push", Repo: "o/r", Ref: ref}
	if n := len(e.OriginThread()); n > originThreadMax {
		t.Fatalf("origin thread is %d bytes", n)
	}
}
