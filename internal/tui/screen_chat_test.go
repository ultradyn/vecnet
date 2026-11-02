package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

const (
	chatOp          = "peer_op"
	chatCodex       = "peer_codex"
	chatGrok        = "peer_grok"
	chatTriage      = "peer_triage"
	chatSleuth      = "peer_sleuth"
	chatClaw        = "peer_claw"
	chatDot         = "peer_dot"
	chatHark        = "thr_01HARK00000000000000003P5R"
	chatOther       = "thr_01TICKET000000000000000018"
	chatApr         = "apr_01HARK"
	chatHostileText = "<img src=x onerror=alert(1)>\x1b]52;c;aGk=\x07\x1b[2Jpwn\u202e"
)

// chatThread is what the scripted hub holds for one thread.
type chatThread struct {
	detail   opapi.ThreadDetail
	messages []envelope.Message
	matrix   opapi.Matrix
}

type chatBlob struct {
	data  []byte
	media string
}

type chatAnswer struct {
	approval opapi.OpenApproval
	option   int
}

// chatAPI is fakeAPI with per-thread responses: it answers by thread id, and
// honours History's after, so a reload does not repeat messages.
type chatAPI struct {
	*fakeAPI
	cmu      sync.Mutex
	byID     map[string]*chatThread
	blobs    map[string]chatBlob
	blobMax  []int64
	answers  []chatAnswer
	sendBack opapi.SendResult
	readIDs  []string
	markNone bool // ReadThread finds nothing unread
}

func newChatAPI() *chatAPI {
	return &chatAPI{fakeAPI: newFake(), byID: map[string]*chatThread{}, blobs: map[string]chatBlob{}}
}

func (a *chatAPI) thread(id string) (*chatThread, error) {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	th := a.byID[id]
	if th == nil {
		return nil, &opclient.Error{Status: 404, Code: "unknown_thread", Message: "no such thread"}
	}
	return th, nil
}

func (a *chatAPI) Thread(ctx context.Context, id string) (opapi.ThreadDetail, error) {
	if err := a.hit(ctx, "Thread"); err != nil {
		return opapi.ThreadDetail{}, err
	}
	th, err := a.thread(id)
	if err != nil {
		return opapi.ThreadDetail{}, err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return th.detail, nil
}

func (a *chatAPI) History(ctx context.Context, id, after string, _ int) (opapi.History, error) {
	if err := a.hit(ctx, "History"); err != nil {
		return opapi.History{}, err
	}
	th, err := a.thread(id)
	if err != nil {
		return opapi.History{}, err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	out := th.messages
	for i, msg := range th.messages {
		if msg.MessageID == after {
			out = th.messages[i+1:]
		}
	}
	return opapi.History{Messages: append([]envelope.Message(nil), out...)}, nil
}

func (a *chatAPI) Matrix(ctx context.Context, id, _ string, _ int) (opapi.Matrix, error) {
	if err := a.hit(ctx, "Matrix"); err != nil {
		return opapi.Matrix{}, err
	}
	th, err := a.thread(id)
	if err != nil {
		return opapi.Matrix{}, err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return th.matrix, nil
}

func (a *chatAPI) ReadThread(ctx context.Context, id string) (opapi.ReadResult, error) {
	a.cmu.Lock()
	a.readIDs = append(a.readIDs, id)
	marked := 1
	if a.markNone {
		marked = 0
	}
	a.cmu.Unlock()
	return opapi.ReadResult{Marked: marked}, a.hit(ctx, "ReadThread")
}

func (a *chatAPI) Send(ctx context.Context, r envelope.SendRequest) (opapi.SendResult, error) {
	if _, err := a.fakeAPI.Send(ctx, r); err != nil {
		return opapi.SendResult{}, err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return a.sendBack, nil
}

func (a *chatAPI) Answer(ctx context.Context, approval opapi.OpenApproval, option int) error {
	a.cmu.Lock()
	a.answers = append(a.answers, chatAnswer{approval, option})
	a.cmu.Unlock()
	return a.hit(ctx, "Answer")
}

func (a *chatAPI) Blob(ctx context.Context, sha string, max int64) ([]byte, string, error) {
	if err := a.hit(ctx, "Blob"); err != nil {
		return nil, "", err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	a.blobMax = append(a.blobMax, max)
	blob, ok := a.blobs[sha]
	if !ok {
		return nil, "", &opclient.Error{Status: 404, Code: "unknown_blob", Message: "no such blob"}
	}
	if int64(len(blob.data)) > max {
		return nil, "", fmt.Errorf("blob exceeds %d bytes", max)
	}
	return blob.data, blob.media, nil
}

func (a *chatAPI) sends() []envelope.SendRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]envelope.SendRequest(nil), a.sent...)
}

func (a *chatAPI) answered() []chatAnswer {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return append([]chatAnswer(nil), a.answers...)
}

func (a *chatAPI) reads() []string {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	return append([]string(nil), a.readIDs...)
}

// add appends a message to a thread, as a delivery would.
func (a *chatAPI) add(id string, msg envelope.Message) {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	a.byID[id].messages = append(a.byID[id].messages, msg)
}

func chatAt(hour, minute int) string {
	return envelope.FormatTime(time.Date(2026, 11, 2, hour, minute, 0, 0, time.UTC))
}

func chatMsg(id, sender, prio, at, text string) envelope.Message {
	return envelope.Message{MessageID: id, ThreadID: chatHark, Kind: "message", Sender: sender, Priority: prio, CreatedAt: at, Content: envelope.Content{Text: text}}
}

func chatPeer(id, name, presence, reach string) opapi.PeerRow {
	return opapi.PeerRow{Peer: opapi.Peer{PeerID: id, Name: name, Presence: presence, Reachability: reach}}
}

func chatCells(cells ...string) []opapi.MatrixCell {
	var out []opapi.MatrixCell
	for i := 0; i+1 < len(cells); i += 2 {
		out = append(out, opapi.MatrixCell{Recipient: cells[i], State: cells[i+1], Display: cells[i+1]})
	}
	return out
}

func chatTitle(s string) *string { return &s }

// chatSeed is the hark thread of c-chat.png and one quieter thread.
func chatSeed() *chatAPI {
	a := newChatAPI()
	a.inbox = opapi.Inbox{Now: envelope.FormatTime(fixedNow), Me: opapi.Me{PeerID: chatOp, Name: "Max", Presence: "working"}}
	a.peers = []opapi.PeerRow{
		chatPeer(chatOp, "max", "working", "connected"),
		chatPeer(chatCodex, "codex-hark", "working", "connected"),
		chatPeer(chatGrok, "grok-reviewer", "unknown", "reachable"),
		chatPeer(chatTriage, "grok-triage", "unknown", "unreachable"),
		chatPeer(chatSleuth, "pag-hark-sleuth", "working", "connected"),
		chatPeer(chatClaw, "claw-ops", "idle", "connected"),
		chatPeer(chatDot, "dot-research", "unknown", "unreachable"),
	}
	budget := 494
	row := opapi.ThreadRow{Thread: envelope.ThreadSummary{ThreadID: chatHark, Title: chatTitle("hark B196: radio end-phrase bug"), Participants: []string{chatCodex, chatGrok, chatOp, chatSleuth, chatClaw, chatDot}, MessageCount: 7, BudgetRemaining: &budget}}
	other := opapi.ThreadRow{Thread: envelope.ThreadSummary{ThreadID: chatOther, Title: chatTitle("Ticket 18: operator UI"), Participants: []string{chatCodex, chatOp}, MessageCount: 1}}
	a.threads = opapi.ThreadPage{Threads: []opapi.ThreadRow{row, other}}

	root := "msg_01root"
	spill := "msg_01spill"
	fix := "msg_01fix"
	first := chatMsg(root, chatCodex, "wake", chatAt(6, 12), "The radio listen did not finalize on \"over\" in session d211530e. I suspect the end-phrase classifier. Can one of you reproduce on a second machine? Log attached.")
	first.Attachments = []envelope.Attachment{{SHA256: "sha-log", Size: 18432, MediaType: "text/plain", Name: "hark-listen.log"}}
	second := chatMsg("msg_01static", chatGrok, "hold", chatAt(6, 14), "No mic on the cloud runner, so I can't exercise the listen path.")
	second.InReplyTo = &root
	third := chatMsg(spill, chatSleuth, "wake_idle", chatAt(6, 31), "Reproduced on x-tuf. "+strings.Repeat("Root cause is in the end-phrase classifier. ", 30))
	third.InReplyTo, third.Metadata = &root, map[string]any{"vecnet.spilled": true}
	third.Content.Truncated = true
	third.Attachments = []envelope.Attachment{{SHA256: "sha-spill", Size: 16000, MediaType: "text/markdown", Name: "message.md"}}
	fourth := chatMsg("msg_01job", chatGrok, "hold", chatAt(6, 52), "Review finished. Two findings, one blocking.")
	fourth.InReplyTo, fourth.Kind = &root, "job_status"
	fourth.JobStatus = &envelope.JobStatus{JobID: "job_01REVIEW000000000000R0T2", Sequence: 3, State: "succeeded", Output: json.RawMessage(`{"findings":2}`)}
	fifth := chatMsg(fix, chatCodex, "wake", chatAt(7, 1), "Patched the anchor and armed the fallback timer in radio mode.")
	fifth.InReplyTo = &spill
	expired := chatMsg("msg_01hub", "hub", "hold", chatAt(7, 3), "")
	expired.Kind = "approval_response"
	expired.ApprovalResponse = &envelope.ApprovalResponse{ApprovalID: "apr_old", Decision: "expired"}
	approval := chatMsg("msg_01apr", chatClaw, "wake", chatAt(7, 5), "May I push the classifier fix to ultradyn/hark master and run the release?")
	approval.InReplyTo, approval.Kind = &fix, "approval_request"
	approval.ApprovalRequest = &envelope.ApprovalRequest{ApprovalID: chatApr, Question: "Push classifier fix to hark master and release?", Options: []string{"yes", "no", "yes but no release"}, ExpiresAt: chatAt(8, 10)}
	a.byID[chatHark] = &chatThread{
		detail: opapi.ThreadDetail{
			Now: envelope.FormatTime(fixedNow), Row: row,
			Files: []opapi.ThreadFile{
				{SHA256: "sha-log", Name: "hark-listen.log", MediaType: "text/plain", MessageID: root, Sender: chatCodex, Size: 18432},
				{SHA256: "sha-spill", Name: "message.md", MediaType: "text/markdown", MessageID: spill, Sender: chatSleuth, Size: 16000, Spilled: true},
			},
			OpenApprovals: []opapi.ThreadApproval{{ApprovalID: chatApr, MessageID: "msg_01apr", ExpiresAt: chatAt(8, 10), Options: []string{"yes", "no", "yes but no release"}, AddressedToMe: true}},
		},
		messages: []envelope.Message{first, second, third, fourth, fifth, expired, approval},
		matrix: opapi.Matrix{Recipients: []string{chatCodex, chatGrok, chatOp, chatSleuth, chatClaw}, Rows: []opapi.MatrixRow{
			{MessageID: root, CreatedAt: chatAt(6, 12), Sender: chatCodex, Cells: chatCells(chatGrok, "processed", chatOp, "read")},
			{MessageID: "msg_01static", CreatedAt: chatAt(6, 14), Sender: chatGrok, Cells: chatCells(chatCodex, "processed", chatOp, "accepted", chatSleuth, "processed")},
			{MessageID: spill, CreatedAt: chatAt(6, 31), Sender: chatSleuth, Cells: chatCells(chatCodex, "processed", chatGrok, "accepted", chatOp, "accepted")},
			{MessageID: fix, CreatedAt: chatAt(7, 1), Sender: chatCodex, Cells: chatCells(chatClaw, "dead_letter", chatGrok, "pushed", chatSleuth, "queued")},
			{MessageID: "msg_01apr", CreatedAt: chatAt(7, 5), Sender: chatClaw, Cells: chatCells(chatOp, "accepted")},
		}},
	}
	note := chatMsg("msg_01note", chatCodex, "hold", chatAt(5, 0), "The dashboard performs no writes.")
	note.ThreadID = chatOther
	a.byID[chatOther] = &chatThread{detail: opapi.ThreadDetail{Now: envelope.FormatTime(fixedNow), Row: other}, messages: []envelope.Message{note}}
	return a
}

// chatModel opens the chat tab on the real screens, at the shot size.
func chatModel(t *testing.T, a *chatAPI, clock *testClock) (*Model, *pump) {
	t.Helper()
	m := newTestModel(t, a, clock)
	p := newPump(t, m)
	p.send(tea.WindowSizeMsg{Width: 140, Height: 42})
	p.run(m.Init())
	p.until("first load", func() bool { return !m.firstLoading() })
	p.until("live", func() bool { return m.Conn == ConnLive })
	p.idle()
	p.press("2")
	first := a.threads.Threads[0].Thread.ThreadID
	p.until("first thread", func() bool {
		st := m.Data.Thread[first]
		return m.CurrentThread == first && st != nil && !st.Loading
	})
	p.idle()
	return m, p
}

func chatOpen(t *testing.T) (*chatAPI, *Model, *pump) {
	t.Helper()
	a := chatSeed()
	m, p := chatModel(t, a, newClock())
	return a, m, p
}

func chatFrame(t *testing.T, m *Model) string {
	t.Helper()
	return plainAt(t, m, 140, 42)
}

func wantFrame(t *testing.T, frame string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
}

func refuseFrame(t *testing.T, frame string, refused ...string) {
	t.Helper()
	for _, s := range refused {
		if strings.Contains(frame, s) {
			t.Errorf("frame must not hold %q:\n%s", s, frame)
		}
	}
}

// chatGolden compares a frame with testdata/chat-<name>.golden.
func chatGolden(t *testing.T, m *Model, name string, w, h int) string {
	t.Helper()
	got := plainAt(t, m, w, h)
	path := filepath.Join("testdata", "chat-"+name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 once the frame is right): %v", err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
	return got
}

var chatSGR = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// assertChatInert fails on anything in a raw frame beyond lipgloss's SGR.
func assertChatInert(t *testing.T, raw string) {
	t.Helper()
	rest := chatSGR.ReplaceAllString(raw, "")
	for i, r := range rest {
		switch {
		case r == '\n':
		case r < 0x20, r >= 0x7f && r <= 0x9f:
			t.Fatalf("control U+%04X at %d: %q", r, i, rest[max(i-20, 0):min(i+20, len(rest))])
		case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
			t.Fatalf("bidi control U+%04X at %d", r, i)
		}
	}
}

// typeText is shared with cmdline_test.go (Task 22), which declares it.

func pressTab(p *pump) { p.send(tea.KeyPressMsg{Code: tea.KeyTab}) }

func waitToast(t *testing.T, p *pump, m *Model, want string) {
	t.Helper()
	p.until("toast "+want, func() bool { return strings.Contains(m.toast.text, want) })
}

func TestChatGolden(t *testing.T) {
	_, m, _ := chatOpen(t)
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		name := fmt.Sprintf("hark-%dx%d", size[0], size[1])
		t.Run(name, func(t *testing.T) {
			frame := chatGolden(t, m, name, size[0], size[1])
			assertFrame(t, frame, size[0], size[1])
			wantFrame(t, frame, "budget 494", "thr_", "<claw-ops>")
		})
	}
	frame := chatFrame(t, m)
	wantFrame(t, frame,
		"hark B196: radio end", "Ticket 18", // the thread list
		"!<codex-hark>", "~<pag-hark-sleuth>", " <grok-reviewer>", // priority glyphs
		"hark-listen.log", "18.0 KiB", // file line
		"spilled", "message.md", // spill line
		"job job_", "succeeded", "seq 3", // job line
		"approval expired",                                     // the hub's quiet line
		"[1]yes", "[2]no", "[3]yes but no", "exp 08:10", "29m", // inline approval
		"grok✓✓", "max●", "codex✓✓", // receipts
		"who", "files",
	)
	refuseFrame(t, frame, "offline")
}

func TestChatPresenceAndReachability(t *testing.T) {
	_, m, _ := chatOpen(t)
	frame := chatFrame(t, m)
	lineOf := func(name string) string {
		for _, line := range strings.Split(frame, "\n") {
			if i := strings.LastIndex(line, "│ ■ "+name); i >= 0 {
				return line[i:]
			}
			if i := strings.LastIndex(line, "■ "+name); i >= 0 {
				return line[i:]
			}
		}
		t.Fatalf("no who row for %s:\n%s", name, frame)
		return ""
	}
	for name, want := range map[string][]string{
		"codex-hark": {"work", "conn"},
		"grok-revi":  {"unkn", "reach"}, // push-only peer: reachable, presence unknown
		"dot-resea":  {"unkn", "unrch"}, // unreachable is not offline
		"claw-ops":   {"idle", "conn"},
	} {
		line := lineOf(name)
		for _, w := range want {
			if !strings.Contains(line, w) {
				t.Errorf("%s: %q lacks %q", name, line, w)
			}
		}
		if strings.Contains(line, "off") {
			t.Errorf("%s reads as offline: %q", name, line)
		}
	}
}

func TestChatNavigation(t *testing.T) {
	a, m, p := chatOpen(t)
	p.press("]")
	p.until("next thread", func() bool {
		st := m.Data.Thread[chatOther]
		return m.CurrentThread == chatOther && st != nil && !st.Loading
	})
	p.idle()
	wantFrame(t, chatFrame(t, m), "The dashboard performs no writes.")
	p.press("]") // the end of the list stays put
	if m.CurrentThread != chatOther {
		t.Fatalf("] past the end moved to %s", m.CurrentThread)
	}
	p.press("[", "[")
	if m.CurrentThread != chatHark {
		t.Fatalf("[ went to %s", m.CurrentThread)
	}
	p.idle()

	// R replies to the selected message: k moves up from the newest.
	p.press("k", "R")
	if m.Mode != ModeInsert {
		t.Fatalf("R: Mode = %v", m.Mode)
	}
	wantFrame(t, chatFrame(t, m), "↳")
	typeText(p, "on it")
	p.press("enter")
	waitToast(t, p, m, "sent")
	p.idle()
	got := a.sends()[0]
	if got.InReplyTo == nil || *got.InReplyTo != "msg_01hub" {
		t.Fatalf("InReplyTo = %v", got.InReplyTo)
	}
	p.press("esc")
	if m.Mode != ModeNormal {
		t.Fatalf("esc: Mode = %v", m.Mode)
	}
	p.press("j", "j", "j") // past the newest stays there
	p.press("k", "k", "k", "k", "k", "k", "k", "k", "k")
	p.press("R")
	typeText(p, "first")
	p.press("enter")
	p.until("second reply", func() bool { return len(a.sends()) == 2 })
	if got := a.sends()[1]; got.InReplyTo == nil || *got.InReplyTo != "msg_01root" {
		t.Fatalf("k to the top: InReplyTo = %v", got.InReplyTo)
	}
}

func TestChatMarksRead(t *testing.T) {
	a, m, p := chatOpen(t)
	p.until("open marks read", func() bool { return len(a.reads()) == 1 })
	if got := a.reads()[0]; got != chatHark {
		t.Fatalf("read %s", got)
	}
	p.idle()
	p.send(Loaded{Key: threadPrefix + chatHark}) // nothing new: no second POST
	if n := len(a.reads()); n != 1 {
		t.Fatalf("read again with nothing new: %d", n)
	}
	// A delivery while the tab shows marks it; one on another tab waits.
	a.add(chatHark, chatMsg("msg_01new", chatCodex, "wake", chatAt(7, 40), "New on this tab."))
	p.send(reloadMsg{tags: []string{threadPrefix + chatHark}})
	p.until("new delivery read", func() bool { return len(a.reads()) == 2 })
	p.idle()
	p.press("1")
	a.add(chatHark, chatMsg("msg_01away", chatCodex, "wake", chatAt(7, 41), "Away from the tab."))
	p.send(reloadMsg{tags: []string{threadPrefix + chatHark}})
	p.until("the away delivery", func() bool {
		msgs := m.Data.Thread[chatHark].Messages
		return msgs[len(msgs)-1].MessageID == "msg_01away"
	})
	p.send(chatTickMsg{}) // sync records a read on this goroutine, before its POST
	if got := chatOf(m).readTo[chatHark]; got == "msg_01away" {
		t.Fatal("marked read while the tab is hidden")
	}
	p.press("2")
	p.until("read on return", func() bool { return len(a.reads()) == 3 })
}

func TestSendFlow(t *testing.T) {
	t.Run("one send", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := a.hold("Send")
		p.press("i")
		if m.Mode != ModeInsert {
			t.Fatalf("i: Mode = %v", m.Mode)
		}
		typeText(p, "q1:?[] hello")
		if m.Tab != tabChat || m.ctx.Err() != nil {
			t.Fatal("insert keys reached the shell")
		}
		wantFrame(t, chatFrame(t, m), "q1:?[] hello")
		p.press("enter")
		p.press("enter", "enter") // a held send takes no second one
		p.until("send", func() bool { return a.count("Send") >= 1 })
		wantFrame(t, chatFrame(t, m), "sending")
		release()
		p.until("sent", func() bool { return strings.Contains(m.toast.text, "sent") })
		p.idle()
		sent := a.sends()
		if len(sent) != 1 {
			t.Fatalf("%d sends", len(sent))
		}
		r := sent[0]
		if r.ThreadID == nil || *r.ThreadID != chatHark || r.Priority != "wake" || r.Content.Text != "q1:?[] hello" || r.InReplyTo != nil || len(r.Recipients) != 0 {
			t.Fatalf("request %+v", r)
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(r.IdempotencyKey) {
			t.Fatalf("key %q fails the schema", r.IdempotencyKey)
		}
		refuseFrame(t, chatFrame(t, m), "q1:?[] hello")
		if m.Mode != ModeInsert {
			t.Fatalf("a send leaves Insert: %v", m.Mode)
		}
		p.press("esc")
		if m.Mode != ModeNormal {
			t.Fatalf("esc: Mode = %v", m.Mode)
		}
	})

	t.Run("text typed during a send stays a draft", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := a.hold("Send")
		p.press("i")
		typeText(p, "first")
		p.press("enter")
		p.until("send", func() bool { return a.count("Send") == 1 })
		typeText(p, " and more")
		release()
		waitToast(t, p, m, "sent")
		wantFrame(t, chatFrame(t, m), "first and more")
	})

	t.Run("empty text sends nothing", func(t *testing.T) {
		a, _, p := chatOpen(t)
		p.press("i")
		typeText(p, "   ")
		p.press("enter")
		p.idle()
		if a.count("Send") != 0 {
			t.Fatal("blank text was sent")
		}
	})

	t.Run("ingress set", func(t *testing.T) {
		var ingress, method, path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ingress, method, path = r.Header.Get("X-Vecnet-Ingress"), r.Method, r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"message_id":"msg_1","thread_id":"`+chatHark+`","deliveries":[],"spilled":false}`)
		}))
		defer srv.Close()
		a := chatSeed()
		m, p := chatModel(t, a, newClock())
		m.API = &wireSend{chatAPI: a, client: opclient.New(srv.URL, "secret")}
		p.press("i")
		typeText(p, "over the wire")
		p.press("enter")
		waitToast(t, p, m, "sent")
		if ingress != "operator_ui" || method != "POST" || path != "/v1/messages" {
			t.Fatalf("ingress %q on %s %s", ingress, method, path)
		}
	})

	t.Run("stable key on retry after a network error", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("dial tcp: connection refused"))
		p.press("i")
		typeText(p, "retry me")
		p.press("enter")
		waitToast(t, p, m, "connection refused")
		wantFrame(t, chatFrame(t, m), "retry me")
		m.toast = toast{}
		p.press("enter")
		waitToast(t, p, m, "connection refused")
		m.toast = toast{}
		a.fail("Send", nil)
		p.press("enter")
		waitToast(t, p, m, "sent")
		sent := a.sends()
		if len(sent) != 3 {
			t.Fatalf("%d sends", len(sent))
		}
		if sent[0].IdempotencyKey != sent[1].IdempotencyKey || sent[1].IdempotencyKey != sent[2].IdempotencyKey {
			t.Fatalf("key changed across retries: %q %q %q", sent[0].IdempotencyKey, sent[1].IdempotencyKey, sent[2].IdempotencyKey)
		}
		typeText(p, "another")
		p.press("enter")
		p.until("next message", func() bool { return len(a.sends()) == 4 })
		if a.sends()[3].IdempotencyKey == sent[0].IdempotencyKey {
			t.Fatal("a new message reused the key")
		}
	})

	t.Run("an edited retry is a new request", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		p.press("i")
		typeText(p, "first draft")
		p.press("enter")
		waitToast(t, p, m, "connection refused")
		typeText(p, "!")
		p.press("enter")
		p.until("edited send", func() bool { return len(a.sends()) == 2 })
		sent := a.sends()
		if sent[0].IdempotencyKey == sent[1].IdempotencyKey {
			t.Fatal("a changed body under one key is idempotency_conflict")
		}
	})

	t.Run("budget error keeps the text", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", &opclient.Error{Status: 422, Code: "thread_budget_exhausted", Message: "thread budget is 0"})
		p.press("i")
		typeText(p, "over budget")
		p.press("enter")
		waitToast(t, p, m, "budget")
		frame := chatFrame(t, m)
		wantFrame(t, frame, "over budget", "thread budget exhausted (budget 494), your text is kept")
		refuseFrame(t, frame, "only the operator")
		if !m.toast.bad {
			t.Fatal("a refused send is not a good toast")
		}
		if m.Mode != ModeInsert {
			t.Fatalf("Mode = %v", m.Mode)
		}
	})

	for _, tc := range []struct {
		err  *opclient.Error
		want string
	}{
		{&opclient.Error{Status: 403, Code: "participants_locked", Message: "this thread is locked"}, "participants_locked: this thread is locked"},
		{&opclient.Error{Status: 403, Code: "scope_denied", Message: "not in scope"}, "scope_denied: not in scope"},
		{&opclient.Error{Status: 429, Code: "rate_limited", Message: "sends rate limit exceeded", Details: map[string]any{"retry_after_seconds": float64(42)}}, "rate_limited: sends rate limit exceeded (retry after 42s)"},
	} {
		t.Run(tc.err.Code, func(t *testing.T) {
			a, m, p := chatOpen(t)
			a.fail("Send", tc.err)
			p.press("i")
			typeText(p, "kept")
			p.press("enter")
			waitToast(t, p, m, tc.want)
			wantFrame(t, chatFrame(t, m), "kept")
		})
	}

	t.Run("a rejected credential stops", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", &opclient.Error{Status: 401, Code: "unauthenticated", Message: "revoked"})
		p.press("i")
		typeText(p, "kept")
		p.press("enter")
		p.until("rejected", func() bool { return m.Conn == ConnRejected })
		wantFrame(t, chatFrame(t, m), "kept")
	})

	t.Run("tab cycles priority", func(t *testing.T) {
		a, m, p := chatOpen(t)
		p.press("i")
		for _, want := range []string{"wake_idle", "hold", "wake", "wake_idle"} {
			pressTab(p)
			if m.Prio != want {
				t.Fatalf("Prio = %q, want %q", m.Prio, want)
			}
		}
		wantFrame(t, chatFrame(t, m), "prio wake_idle")
		typeText(p, "later")
		p.press("enter")
		p.until("send", func() bool { return len(a.sends()) == 1 })
		if got := a.sends()[0].Priority; got != "wake_idle" {
			t.Fatalf("Priority = %q", got)
		}
	})

	t.Run("paste is cleaned", func(t *testing.T) {
		a, _, p := chatOpen(t)
		p.press("i")
		p.send(tea.PasteMsg{Content: "pasted\x1b[2J\nline"})
		p.press("enter")
		p.until("send", func() bool { return len(a.sends()) == 1 })
		if got := a.sends()[0].Content.Text; got != "pasted\nline" {
			t.Fatalf("text %q", got)
		}
	})
}

// wireSend sends through the real client, so a test can see the HTTP request.
type wireSend struct {
	*chatAPI
	client *opclient.Client
}

func (w *wireSend) Send(ctx context.Context, r envelope.SendRequest) (opapi.SendResult, error) {
	return w.client.Send(ctx, r)
}

// chatDanger closes the seed's approval and adds a closed lint card, filler
// messages, then one open approval far from the top: the case where what the
// operator is looking at and what a digit would answer can differ.
func chatDanger(a *chatAPI, filler int) {
	card := func(id, approval, question, expires string) envelope.Message {
		msg := chatMsg(id, chatClaw, "wake", chatAt(7, 10), question)
		msg.Kind = "approval_request"
		msg.ApprovalRequest = &envelope.ApprovalRequest{ApprovalID: approval, Question: question, Options: []string{"yes", "no"}, ExpiresAt: expires}
		return msg
	}
	a.add(chatHark, card("msg_01lint", "apr_lint", "Run the harmless lint?", chatAt(7, 20)))
	for i := range filler {
		a.add(chatHark, chatMsg(fmt.Sprintf("msg_fill%02d", i), chatCodex, "hold", chatAt(7, 12), "filler"))
	}
	a.add(chatHark, card("msg_01drop", "apr_drop", "Delete the production database?", chatAt(8, 10)))
	a.byID[chatHark].detail.OpenApprovals = []opapi.ThreadApproval{{ApprovalID: "apr_drop", MessageID: "msg_01drop", ExpiresAt: chatAt(8, 10), Options: []string{"yes", "no"}, AddressedToMe: true}}
}

func chatOf(m *Model) *chatScreen { return m.screens[tabChat].(*chatScreen) }

func TestChatAnswer(t *testing.T) {
	const (
		dropPrompt = "answer: Delete the production database?  1 yes  2 no"
		dropCard   = "? Delete the production database? [1]yes [2]no"
	)
	t.Run("a closed card selected: the digit's target is named and selected", func(t *testing.T) {
		a := chatSeed()
		chatDanger(a, 0)
		m, p := chatModel(t, a, newClock())
		p.press("k")
		wantFrame(t, chatFrame(t, m), "Run the harmless lint?")
		if got := chatOf(m).sel; got != "msg_01lint" {
			t.Fatalf("selection %q", got)
		}
		p.press("a")
		wantFrame(t, chatFrame(t, m), dropPrompt, dropCard)
		if got := chatOf(m).sel; got != "msg_01drop" {
			t.Fatalf("the prompt answers msg_01drop, the selection is on %q", got)
		}
		p.press("1")
		p.until("answer", func() bool { return len(a.answered()) == 1 })
		if got := a.answered()[0]; got.approval.ApprovalID != "apr_drop" || got.option != 0 {
			t.Fatalf("answer %+v", got)
		}
	})
	t.Run("the selected card is answered, not the newest", func(t *testing.T) {
		a := chatSeed()
		chatDanger(a, 0)
		th := a.byID[chatHark]
		th.detail.OpenApprovals = append([]opapi.ThreadApproval{{ApprovalID: "apr_lint", MessageID: "msg_01lint", ExpiresAt: chatAt(8, 5), Options: []string{"yes", "no"}, AddressedToMe: true}}, th.detail.OpenApprovals...)
		m, p := chatModel(t, a, newClock())
		p.press("k", "a")
		wantFrame(t, chatFrame(t, m), "answer: Run the harmless lint?  1 yes  2 no")
		p.press("2")
		p.until("answer", func() bool { return len(a.answered()) == 1 })
		if got := a.answered()[0]; got.approval.ApprovalID != "apr_lint" || got.option != 1 {
			t.Fatalf("answer %+v", got)
		}
	})
	t.Run("opening another thread drops the prompt", func(t *testing.T) {
		a, m, p := chatOpen(t)
		p.press("a")
		p.run(m.OpenThread(chatOther))
		p.until("the other thread", func() bool { return chatOf(m).thread == chatOther })
		if chatOf(m).prompt != nil || m.Mode != ModeNormal {
			t.Fatalf("prompt %+v, mode %v", chatOf(m).prompt, m.Mode)
		}
		p.press("1")
		p.idle()
		if len(a.answered()) != 0 {
			t.Fatalf("answers %+v", a.answered())
		}
	})
	t.Run("an approval expiring under the prompt takes no digit", func(t *testing.T) {
		a := chatSeed()
		clock := newClock()
		m, p := chatModel(t, a, clock)
		p.press("a")
		clock.advance(time.Hour)
		p.press("1")
		waitToast(t, p, m, "expired")
		p.idle()
		if len(a.answered()) != 0 {
			t.Fatalf("answers %+v", a.answered())
		}
	})
	t.Run("a rejected credential stops the answer", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Answer", &opclient.Error{Status: 401, Code: "unauthenticated", Message: "revoked"})
		p.press("a", "1")
		p.until("rejected", func() bool { return m.Conn == ConnRejected })
		wantFrame(t, chatFrame(t, m), "[1]yes")
	})
	t.Run("an answer reloads the thread", func(t *testing.T) {
		a := chatSeed()
		a.markNone = true // so that opening the thread leaves no reload about
		m, p := chatModel(t, a, newClock())
		p.until("open marks read", func() bool { return len(a.reads()) == 1 })
		p.idle()
		before := a.count("Thread")
		p.press("a", "1")
		waitToast(t, p, m, "answered")
		p.until("the reload", func() bool { return a.count("Thread") > before })
	})
	t.Run("an off-screen approval comes on screen before the digit", func(t *testing.T) {
		a := chatSeed()
		chatDanger(a, 40)
		m, p := chatModel(t, a, newClock())
		for range 60 {
			p.press("k")
		}
		refuseFrame(t, chatFrame(t, m), "Delete the production database?")
		p.press("a")
		wantFrame(t, chatFrame(t, m), dropPrompt, dropCard)
		p.press("x") // cancelled: nothing went, and the card stays where it was put
		p.idle()
		if len(a.answered()) != 0 {
			t.Fatalf("answers %+v", a.answered())
		}
		wantFrame(t, chatFrame(t, m), dropCard)
		refuseFrame(t, chatFrame(t, m), "answer:")
	})
	t.Run("a message landing under the prompt leaves the card selected", func(t *testing.T) {
		a := chatSeed()
		chatDanger(a, 0)
		m, p := chatModel(t, a, newClock())
		p.press("a")
		s := chatOf(m)
		a.add(chatHark, chatMsg("msg_01late", chatCodex, "wake", chatAt(7, 40), "Landed under the prompt."))
		p.send(reloadMsg{tags: []string{threadPrefix + chatHark}})
		p.until("delivery", func() bool { return len(m.Data.Thread[chatHark].Messages) == 10 })
		if got := s.selected(m.Data.Thread[chatHark]).MessageID; got != "msg_01drop" {
			t.Fatalf("selection moved to %q under the prompt", got)
		}
		p.press("esc") // back to riding the newest message, as before a
		if got := s.selected(m.Data.Thread[chatHark]).MessageID; got != "msg_01late" {
			t.Fatalf("selection stuck on %q", got)
		}
	})
	t.Run("a long question is cut, the options stay", func(t *testing.T) {
		a := chatSeed()
		long := strings.Repeat("Should this really go ahead? ", 20)
		a.byID[chatHark].messages[6].ApprovalRequest.Question = long
		m, p := chatModel(t, a, newClock())
		p.press("a")
		frame := plainAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		wantFrame(t, frame, "answer: Should this really go ahead?", "…  1 yes  2 no")
	})
	t.Run("options too long for the line still leave the question", func(t *testing.T) {
		a := chatSeed()
		options := []string{strings.Repeat("first ", 10), strings.Repeat("second ", 10), "no"}
		a.byID[chatHark].messages[6].ApprovalRequest.Options = options
		a.byID[chatHark].detail.OpenApprovals[0].Options = options
		m, p := chatModel(t, a, newClock())
		p.press("a")
		frame := plainAt(t, m, 80, 24)
		assertFrame(t, frame, 80, 24)
		wantFrame(t, frame, "answer: Push classifier fix to", "1 first")
	})
	t.Run("digit answers once", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := a.hold("Answer")
		p.press("a")
		wantFrame(t, chatFrame(t, m), "1 yes", "2 no", "3 yes but no release")
		p.press("3")
		if m.Tab != tabChat {
			t.Fatal("the digit reached the shell and switched tabs")
		}
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v", m.Mode)
		}
		p.press("a") // the answer is on its way: no second prompt
		if m.Mode != ModeNormal {
			t.Fatalf("a second prompt opened: %v", m.Mode)
		}
		p.until("answer", func() bool { return a.count("Answer") >= 1 })
		release()
		waitToast(t, p, m, "answered")
		p.idle()
		got := a.answered()
		if len(got) != 1 {
			t.Fatalf("%d answers", len(got))
		}
		ap := got[0].approval
		if got[0].option != 2 || ap.ApprovalID != chatApr || ap.ThreadID != chatHark || ap.MessageID != "msg_01apr" || ap.Requester != chatClaw || len(ap.Options) != 3 {
			t.Fatalf("answer %+v", got[0])
		}
	})
	t.Run("out of range and other keys do nothing", func(t *testing.T) {
		a, m, p := chatOpen(t)
		p.press("a", "9")
		p.press("a", "esc")
		p.press("a", "x")
		p.idle()
		if len(a.answered()) != 0 {
			t.Fatalf("answers %+v", a.answered())
		}
		if m.Mode != ModeNormal || m.Tab != tabChat {
			t.Fatalf("mode %v tab %d", m.Mode, m.Tab)
		}
	})
	t.Run("a closed approval never retries or claims success", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Answer", &opclient.Error{Status: 422, Code: "approval_closed", Message: "closed"})
		before := a.count("Inbox")
		p.press("a", "1")
		waitToast(t, p, m, "closed")
		if !m.toast.bad {
			t.Fatal("approval_closed must not read as success")
		}
		p.until("reload", func() bool { return a.count("Inbox") > before })
		p.idle()
		p.press("a")
		if m.Mode != ModeNormal {
			t.Fatalf("a prompt opened on a closed approval: %v", m.Mode)
		}
		p.idle()
		if len(a.answered()) != 1 {
			t.Fatalf("%d answers", len(a.answered()))
		}
		frame := chatFrame(t, m)
		wantFrame(t, frame, "closed")
		refuseFrame(t, frame, "[1]yes")
	})
	t.Run("hub clock decides expiry", func(t *testing.T) {
		a := chatSeed()
		clock := newClock()
		m, p := chatModel(t, a, clock)
		// A client clock two hours fast still shows the hub's countdown.
		a.cmu.Lock()
		th := a.byID[chatHark]
		a.cmu.Unlock()
		clock.advance(2 * time.Hour)
		m.noteHubNow(th.detail.Now, clock.Now())
		frame := chatFrame(t, m)
		wantFrame(t, frame, "29m", "[1]yes")
		// Past expiry on the hub's clock the key is refused locally.
		clock.advance(30 * time.Minute)
		p.press("a")
		waitToast(t, p, m, "expired")
		if m.Mode != ModeNormal {
			t.Fatalf("a prompt opened on an expired approval: %v", m.Mode)
		}
		frame = chatFrame(t, m)
		wantFrame(t, frame, "release? expired")
		refuseFrame(t, frame, "[1]yes")
	})
	t.Run("not addressed to the operator is read-only", func(t *testing.T) {
		a := chatSeed()
		a.byID[chatHark].detail.OpenApprovals[0].AddressedToMe = false
		m, p := chatModel(t, a, newClock())
		frame := chatFrame(t, m)
		refuseFrame(t, frame, "[1]yes")
		wantFrame(t, frame, "read-only")
		p.press("a")
		waitToast(t, p, m, "no open approval")
		if m.Mode != ModeNormal {
			t.Fatalf("a prompt opened on a read-only approval: %v", m.Mode)
		}
	})
}

func TestMatrixOverlay(t *testing.T) {
	t.Run("cells by display", func(t *testing.T) {
		_, m, p := chatOpen(t)
		p.press("m")
		frame := chatFrame(t, m)
		assertFrame(t, frame, 140, 42)
		wantFrame(t, frame, "delivery matrix", "codex-hark", "grok-reviewer", "processed", "read", "accepted", "dead_letter", "pushed", "queued")
		refuseFrame(t, frame, "The radio listen")
		p.press("esc")
		frame = chatFrame(t, m)
		refuseFrame(t, frame, "delivery matrix")
		wantFrame(t, frame, "The radio listen")
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v", m.Mode)
		}
	})
	t.Run("read shows by display, whatever the state", func(t *testing.T) {
		a := chatSeed()
		// §2.13: display is read when state is accepted or pushed and read_at is set.
		at := chatAt(6, 20)
		a.byID[chatHark].matrix.Rows = []opapi.MatrixRow{{MessageID: "msg_01root", CreatedAt: chatAt(6, 12), Sender: chatCodex, Cells: []opapi.MatrixCell{
			{Recipient: chatGrok, State: "accepted", Display: "read", ReadAt: &at},
			{Recipient: chatOp, State: "pushed", Display: "read", ReadAt: &at},
		}}}
		m, p := chatModel(t, a, newClock())
		frame := chatFrame(t, m)
		wantFrame(t, frame, "grok● max●")
		refuseFrame(t, frame, "grok✓", "max›")
		p.press("m")
		frame = chatFrame(t, m)
		i := strings.Index(frame, "06:12")
		if i < 0 {
			t.Fatalf("no row for the first message:\n%s", frame)
		}
		row, _, _ := strings.Cut(frame[i:], "\n")
		if strings.Count(row, "read") != 2 || strings.Contains(row, "accepted") || strings.Contains(row, "pushed") {
			t.Fatalf("matrix row %q", row)
		}
	})
	t.Run("64 recipients scroll", func(t *testing.T) {
		a := chatSeed()
		var names []string
		var recipients []string
		var cells []opapi.MatrixCell
		for i := range 64 {
			id := fmt.Sprintf("peer_scale%02d", i)
			name := fmt.Sprintf("scale-%02d", i)
			names, recipients = append(names, name), append(recipients, id)
			a.peers = append(a.peers, chatPeer(id, name, "unknown", "unknown"))
			cells = append(cells, opapi.MatrixCell{Recipient: id, State: "queued", Display: "queued"})
		}
		var rows []opapi.MatrixRow
		for i := range 60 {
			rows = append(rows, opapi.MatrixRow{MessageID: fmt.Sprintf("msg_%03d", i), CreatedAt: chatAt(5, i), Sender: chatOp, Cells: cells})
		}
		a.byID[chatHark].matrix = opapi.Matrix{Recipients: recipients, Rows: rows}
		m, p := chatModel(t, a, newClock())
		p.press("m")
		frame := chatFrame(t, m)
		assertFrame(t, frame, 140, 42)
		wantFrame(t, frame, names[0], "of 64", "05:59") // newest rows first in view
		refuseFrame(t, frame, names[63])
		for range 70 {
			p.press("l")
		}
		frame = chatFrame(t, m)
		assertFrame(t, frame, 140, 42)
		wantFrame(t, frame, names[63], "of 64")
		refuseFrame(t, frame, names[0])
		for range 70 {
			p.press("h")
		}
		wantFrame(t, chatFrame(t, m), names[0])
		for range 70 {
			p.press("k")
		}
		frame = chatFrame(t, m)
		wantFrame(t, frame, "05:00")
		refuseFrame(t, frame, "05:59")
		for range 70 {
			p.press("j")
		}
		wantFrame(t, chatFrame(t, m), "05:59")
	})
}

// chatSpilled makes the newest hark message a spilled one.
func chatSpilled(a *chatAPI, att envelope.Attachment) {
	msg := chatMsg("msg_01long", chatSleuth, "wake", chatAt(7, 30), "the preview")
	msg.Metadata = map[string]any{"vecnet.spilled": true}
	msg.Content.Truncated = true
	msg.Attachments = []envelope.Attachment{att}
	a.add(chatHark, msg)
}

func TestExpandSpill(t *testing.T) {
	t.Run("body with escapes rendered clean", func(t *testing.T) {
		a := chatSeed()
		body := "full text\n" + chatHostileText + "\nthe last line"
		a.blobs["sha-long"] = chatBlob{[]byte(body), "text/markdown; charset=utf-8"}
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: int64(len(body)), MediaType: "text/markdown", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		p.until("body", func() bool { return strings.Contains(chatFrame(t, m), "full text") })
		frame := chatFrame(t, m)
		wantFrame(t, frame, "pwn", "the last line", "<img src=x onerror=alert(1)>")
		assertFrame(t, frame, 140, 42)
		assertChatInert(t, m.View().Content)
		if a.blobMax[0] != 1<<20 {
			t.Fatalf("Blob max = %d", a.blobMax[0])
		}
		p.press("esc")
		frame = chatFrame(t, m)
		refuseFrame(t, frame, "the last line")
		wantFrame(t, frame, "the preview")
	})
	t.Run("long body scrolls", func(t *testing.T) {
		a := chatSeed()
		var body strings.Builder
		for i := range 200 {
			fmt.Fprintf(&body, "line %03d\n", i)
		}
		a.blobs["sha-long"] = chatBlob{[]byte(body.String()), "text/plain"}
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: int64(body.Len()), MediaType: "text/plain", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		p.until("body", func() bool { return strings.Contains(chatFrame(t, m), "line 000") })
		refuseFrame(t, chatFrame(t, m), "line 199")
		for range 220 {
			p.press("j")
		}
		frame := chatFrame(t, m)
		wantFrame(t, frame, "line 199")
		refuseFrame(t, frame, "line 000")
	})
	t.Run("binary media type refused", func(t *testing.T) {
		a := chatSeed()
		a.blobs["sha-long"] = chatBlob{[]byte("\x00\x01"), "application/octet-stream"}
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: 2, MediaType: "application/octet-stream", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		waitToast(t, p, m, "refused")
		if a.count("Blob") != 0 {
			t.Fatal("refused media type was fetched")
		}
	})
	t.Run("hub says binary", func(t *testing.T) {
		a := chatSeed()
		a.blobs["sha-long"] = chatBlob{[]byte("MZ\x90"), "application/x-dosexec"}
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: 3, MediaType: "text/plain", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		waitToast(t, p, m, "refused")
		refuseFrame(t, chatFrame(t, m), "MZ")
	})
	t.Run("over 1 MiB refused", func(t *testing.T) {
		a := chatSeed()
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: 1<<20 + 1, MediaType: "text/markdown", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		waitToast(t, p, m, "refused")
		if a.count("Blob") != 0 {
			t.Fatal("oversized blob was fetched")
		}
	})
	t.Run("understated size refused", func(t *testing.T) {
		a := chatSeed()
		a.blobs["sha-long"] = chatBlob{bytes.Repeat([]byte("x"), 1<<20+1), "text/plain"}
		chatSpilled(a, envelope.Attachment{SHA256: "sha-long", Size: 10, MediaType: "text/plain", Name: "message.md"})
		m, p := chatModel(t, a, newClock())
		p.press("e")
		waitToast(t, p, m, "refused")
		refuseFrame(t, chatFrame(t, m), "xxxxxxxxxx")
	})
}

// chatFile makes the newest hark message carry one file named name.
func chatFile(a *chatAPI, name string, data string) {
	msg := chatMsg("msg_01file", chatSleuth, "wake", chatAt(7, 30), "a file")
	msg.Attachments = []envelope.Attachment{{SHA256: "sha-file", Size: int64(len(data)), MediaType: "text/plain", Name: name}}
	a.add(chatHark, msg)
	a.blobs["sha-file"] = chatBlob{[]byte(data), "text/plain"}
}

func TestChatTallMessage(t *testing.T) {
	a := chatSeed()
	a.add(chatHark, chatMsg("msg_01tall", chatCodex, "hold", chatAt(7, 30), strings.Repeat("line\n", 60)+"the end"))
	m, p := chatModel(t, a, newClock())
	frame := plainAt(t, m, 140, 42)
	if !regexp.MustCompile(`\[\+\d+ lines, e reads it all\]`).MatchString(frame) {
		t.Fatalf("no hint under a cut message:\n%s", frame)
	}
	p.press("e")
	refuseFrame(t, chatFrame(t, m), "e reads it all")
	wantFrame(t, plainAt(t, m, 80, 24), "j/k pgup/pgdn esc  ")
}

func TestSaveFileNameSafe(t *testing.T) {
	saved := func(t *testing.T, name string) (string, *Model, *pump) {
		t.Helper()
		parent := t.TempDir()
		dir := filepath.Join(parent, "cwd")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		a := chatSeed()
		chatFile(a, name, "contents")
		m, p := chatModel(t, a, newClock())
		return dir, m, p
	}
	entries := func(t *testing.T, dir string) string {
		t.Helper()
		list, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range list {
			names = append(names, e.Name())
		}
		return strings.Join(names, "|")
	}

	t.Run("traversal and escapes", func(t *testing.T) {
		dir, m, p := saved(t, "../../x\x1b[31m")
		p.press("s")
		waitToast(t, p, m, "saved")
		if got := entries(t, dir); got != "x" {
			t.Fatalf("cwd holds %q", got)
		}
		if got := entries(t, filepath.Dir(dir)); got != "cwd" {
			t.Fatalf("escaped the working directory: %q", got)
		}
		data, err := os.ReadFile(filepath.Join(dir, "x"))
		if err != nil || string(data) != "contents" {
			t.Fatalf("read %q, %v", data, err)
		}
		info, err := os.Stat(filepath.Join(dir, "x"))
		if err != nil || info.Mode()&0o111 != 0 {
			t.Fatalf("mode %v, %v", info.Mode(), err)
		}
	})
	t.Run("existing file not overwritten", func(t *testing.T) {
		dir, m, p := saved(t, "../../x\x1b[31m")
		if err := os.WriteFile(filepath.Join(dir, "x"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		p.press("s")
		waitToast(t, p, m, "saved")
		m.toast = toast{}
		p.press("s")
		waitToast(t, p, m, "saved")
		if got := entries(t, dir); got != "x|x (1)|x (2)" {
			t.Fatalf("cwd holds %q", got)
		}
		if data, _ := os.ReadFile(filepath.Join(dir, "x")); string(data) != "mine" {
			t.Fatalf("overwritten: %q", data)
		}
	})
	t.Run("extension stays last", func(t *testing.T) {
		dir, m, p := saved(t, "name.ext")
		if err := os.WriteFile(filepath.Join(dir, "name.ext"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		p.press("s")
		waitToast(t, p, m, "saved")
		if got := entries(t, dir); got != "name (1).ext|name.ext" {
			t.Fatalf("cwd holds %q", got)
		}
	})
	t.Run("a dangling link is not followed", func(t *testing.T) {
		dir, m, p := saved(t, "link")
		target := filepath.Join(filepath.Dir(dir), "outside")
		if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		p.press("s")
		waitToast(t, p, m, "saved")
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wrote through the link: %v", err)
		}
		if got := entries(t, dir); got != "link|link (1)" {
			t.Fatalf("cwd holds %q", got)
		}
	})
	for _, name := range []string{"..", "/", "", "\x1b[31m"} {
		t.Run(fmt.Sprintf("no usable name %q", name), func(t *testing.T) {
			dir, m, p := saved(t, name)
			p.press("s")
			waitToast(t, p, m, "saved")
			if got := entries(t, dir); got != "blob-sha-file" {
				t.Fatalf("cwd holds %q", got)
			}
		})
	}
	t.Run("a file over the reader's cap saves whole", func(t *testing.T) {
		data := strings.Repeat("x", 2<<20)
		dir, m, p := saved(t, "big.bin")
		a := m.API.(*chatAPI)
		a.cmu.Lock()
		a.blobs["sha-file"] = chatBlob{[]byte(data), "application/octet-stream"}
		a.cmu.Unlock()
		p.press("s")
		waitToast(t, p, m, "saved")
		info, err := os.Stat(filepath.Join(dir, "big.bin"))
		if err != nil || info.Size() != int64(len(data)) {
			t.Fatalf("stat %v, %v", info, err)
		}
	})
	t.Run("purged file is refused", func(t *testing.T) {
		t.Chdir(t.TempDir())
		a := chatSeed()
		chatFile(a, "gone.txt", "contents")
		a.byID[chatHark].detail.Files = append(a.byID[chatHark].detail.Files, opapi.ThreadFile{SHA256: "sha-file", Name: "gone.txt", MessageID: "msg_01file", Purged: true})
		m, p := chatModel(t, a, newClock())
		p.press("s")
		waitToast(t, p, m, "purged")
		if a.count("Blob") != 0 {
			t.Fatal("purged blob was fetched")
		}
	})
	t.Run("no file on the message", func(t *testing.T) {
		a, m, p := chatOpen(t)
		p.press("s")
		waitToast(t, p, m, "no file")
		if a.count("Blob") != 0 {
			t.Fatal("fetched a blob")
		}
	})
	t.Run("f picks among several", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		a := chatSeed()
		msg := chatMsg("msg_01two", chatSleuth, "wake", chatAt(7, 30), "two files")
		msg.Attachments = []envelope.Attachment{{SHA256: "sha-a", Size: 1, MediaType: "text/plain", Name: "a.txt"}, {SHA256: "sha-b", Size: 1, MediaType: "text/plain", Name: "b.txt"}}
		a.add(chatHark, msg)
		a.blobs["sha-a"], a.blobs["sha-b"] = chatBlob{[]byte("a"), "text/plain"}, chatBlob{[]byte("b"), "text/plain"}
		m, p := chatModel(t, a, newClock())
		p.press("f", "s")
		waitToast(t, p, m, "saved")
		if got := entries(t, dir); got != "b.txt" {
			t.Fatalf("cwd holds %q", got)
		}
	})
}

func TestMsgCommand(t *testing.T) {
	run := func(t *testing.T, m *Model, p *pump, args ...string) {
		t.Helper()
		cmd, ok := m.Commands["msg"]
		if !ok {
			t.Fatal(":msg is not registered")
		}
		p.run(cmd.Run(m, args))
	}
	// kept waits for the failed line to come back, with its reason on screen:
	// the open command line covers the toast.
	kept := func(t *testing.T, p *pump, m *Model, input string, reason ...string) {
		t.Helper()
		p.until("the line to reopen", func() bool { return m.Mode == ModeCommand && m.Input == input })
		frame := chatFrame(t, m)
		wantFrame(t, frame, ":"+input)
		wantFrame(t, frame, reason...)
	}
	t.Run("ambiguous prefix", func(t *testing.T) {
		a, m, p := chatOpen(t)
		run(t, m, p, "grok", "hello", "there")
		kept(t, p, m, "msg grok hello there", "ambiguous prefix grok: grok-reviewer, grok-triage")
		if a.count("Send") != 0 {
			t.Fatal("sent on an ambiguous prefix")
		}
	})
	t.Run("no match", func(t *testing.T) {
		a, m, p := chatOpen(t)
		run(t, m, p, "zzz", "hello")
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
		if a.count("Send") != 0 {
			t.Fatal("sent with no peer")
		}
	})
	t.Run("missing text", func(t *testing.T) {
		a, m, p := chatOpen(t)
		run(t, m, p, "codex")
		kept(t, p, m, "msg codex", "usage: msg <peer-prefix> <text>")
		if a.count("Send") != 0 {
			t.Fatal("sent nothing to say")
		}
	})
	t.Run("the shell closing the line after Run does not lose the text", func(t *testing.T) {
		a, m, p := chatOpen(t)
		m.Mode, m.Input = ModeCommand, "msg zzz hello"
		cmd := m.Commands["msg"].Run(m, []string{"zzz", "hello"})
		m.Mode, m.Input = ModeNormal, ""
		p.run(cmd)
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
		if a.count("Send") != 0 {
			t.Fatal("sent with no peer")
		}
	})
	t.Run("from another tab the reason still shows", func(t *testing.T) {
		_, m, p := chatOpen(t)
		p.press("1")
		run(t, m, p, "zzz", "hello")
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
		if m.Tab != tabChat {
			t.Fatalf("tab %d", m.Tab)
		}
	})
	t.Run("an overlay gives way to the reason", func(t *testing.T) {
		_, m, p := chatOpen(t)
		p.press("m")
		run(t, m, p, "zzz", "hello")
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
		refuseFrame(t, chatFrame(t, m), "delivery matrix")
	})
	t.Run("the reason goes with the line", func(t *testing.T) {
		_, m, p := chatOpen(t)
		run(t, m, p, "zzz", "hello")
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
		m.toast = toast{}
		p.press("esc")
		p.until("the reason to clear", func() bool { return chatOf(m).msgErr == "" })
		refuseFrame(t, chatFrame(t, m), "no peer matches")
		p.press(":")
		refuseFrame(t, chatFrame(t, m), "no peer matches")
	})
	// failing starts a :msg to claw that fails once release is called.
	failing := func(t *testing.T, a *chatAPI, m *Model, p *pump) (release func()) {
		t.Helper()
		a.fail("Send", errors.New("connection refused"))
		release = a.hold("Send")
		run(t, m, p, "claw", "keep", "this")
		p.until("send", func() bool { return a.count("Send") == 1 })
		return release
	}
	const line, later = "msg claw keep this", "returns to the command line"
	t.Run("a failure in Insert waits for the operator to leave it", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := failing(t, a, m, p)
		p.press("i")
		typeText(p, "a draft")
		release()
		waitToast(t, p, m, "connection refused")
		frame := chatFrame(t, m)
		wantFrame(t, frame, "a draft", later)
		refuseFrame(t, frame, "enter retries")
		if m.Mode != ModeInsert {
			t.Fatalf("Mode = %v", m.Mode)
		}
		p.press("esc")
		kept(t, p, m, line, "connection refused", "enter retries")
		refuseFrame(t, chatFrame(t, m), later)
		if d := chatOf(m).draft(chatHark); d.text != "a draft" {
			t.Fatalf("draft %q", d.text)
		}
		p.press("esc")
		p.send(chatTickMsg{})
		if m.Mode != ModeNormal {
			t.Fatalf("a kept line came back twice: %v", m.Mode)
		}
	})
	t.Run("a failure under an answer prompt waits for the prompt", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := failing(t, a, m, p)
		p.press("a")
		release()
		waitToast(t, p, m, "connection refused")
		if m.Mode != ModeInsert || chatOf(m).prompt == nil {
			t.Fatalf("Mode = %v, prompt %+v", m.Mode, chatOf(m).prompt)
		}
		p.press("x")
		kept(t, p, m, line, "connection refused")
	})
	t.Run("a failure on an open line waits for that line", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := failing(t, a, m, p)
		p.press(":")
		typeText(p, "other")
		release()
		p.until("the failure", func() bool { return chatOf(m).msgErr != "" })
		if m.Mode != ModeCommand || m.Input != "other" {
			t.Fatalf("Mode %v, Input %q", m.Mode, m.Input)
		}
		wantFrame(t, chatFrame(t, m), ":other", "connection refused", later)
		p.press("esc")
		kept(t, p, m, line, "connection refused", "enter retries")
		refuseFrame(t, chatFrame(t, m), later)
	})
	t.Run("a failure behind help waits for help", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := failing(t, a, m, p)
		p.press("?")
		release()
		waitToast(t, p, m, later)
		if m.Mode != ModeNormal || !m.Help {
			t.Fatalf("Mode %v, Help %v", m.Mode, m.Help)
		}
		p.press("x")
		kept(t, p, m, line, "connection refused")
	})
	t.Run("two failures come back one at a time", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := failing(t, a, m, p)
		p.press("i")
		release()
		p.until("the first failure", func() bool { return len(chatOf(m).heldMsgs) == 1 })
		run(t, m, p, "codex", "second")
		p.until("the second failure", func() bool { return len(chatOf(m).heldMsgs) == 2 })
		p.press("esc")
		kept(t, p, m, line, "connection refused")
		p.press("esc")
		kept(t, p, m, "msg codex second", "connection refused")
		p.press("esc")
		p.send(chatTickMsg{})
		if m.Mode != ModeNormal {
			t.Fatalf("Mode = %v", m.Mode)
		}
	})
	t.Run("a refusal in Insert waits too", func(t *testing.T) {
		_, m, p := chatOpen(t)
		p.press("i")
		run(t, m, p, "zzz", "hello")
		waitToast(t, p, m, later)
		wantFrame(t, chatFrame(t, m), "no peer matches zzz")
		p.press("esc")
		kept(t, p, m, "msg zzz hello", "no peer matches zzz")
	})
	t.Run("an edited retry takes a new key", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		run(t, m, p, "claw", "keep", "this")
		kept(t, p, m, line, "connection refused")
		m.Mode, m.Input = ModeNormal, ""
		run(t, m, p, "claw", "keep", "that")
		p.until("edited retry", func() bool { return len(a.sends()) == 2 })
		sent := a.sends()
		if sent[0].IdempotencyKey == sent[1].IdempotencyKey {
			t.Fatal("a changed body under one key is idempotency_conflict")
		}
	})
	t.Run("unique prefix sends by peer id", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.sendBack = opapi.SendResult{MessageID: "msg_x", ThreadID: chatOther}
		m.Prio = "hold"
		run(t, m, p, "CODEX", "hello", "there")
		p.until("send", func() bool { return len(a.sends()) == 1 })
		r := a.sends()[0]
		if r.ThreadID != nil || len(r.Recipients) != 1 || r.Recipients[0] != chatCodex || r.Content.Text != "hello there" || r.Priority != "hold" {
			t.Fatalf("request %+v", r)
		}
		p.until("opens the new thread", func() bool { return m.CurrentThread == chatOther })
	})
	t.Run("peer id prefix", func(t *testing.T) {
		a, m, p := chatOpen(t)
		run(t, m, p, "peer_cl", "hi")
		p.until("send", func() bool { return len(a.sends()) == 1 })
		if r := a.sends()[0]; r.Recipients[0] != chatClaw {
			t.Fatalf("request %+v", r)
		}
	})
	t.Run("failure keeps the text and the key", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		run(t, m, p, "claw", "keep", "this")
		kept(t, p, m, "msg claw keep this", "connection refused")
		m.Mode, m.Input = ModeNormal, ""
		a.fail("Send", nil)
		run(t, m, p, "claw", "keep", "this")
		p.until("retry", func() bool { return len(a.sends()) == 2 })
		sent := a.sends()
		if sent[0].IdempotencyKey != sent[1].IdempotencyKey {
			t.Fatalf("key changed: %q %q", sent[0].IdempotencyKey, sent[1].IdempotencyKey)
		}
	})
}

func TestChatHostile(t *testing.T) {
	a := chatSeed()
	const hostile = chatHostileText
	budget := 3
	group := hostile
	row := opapi.ThreadRow{Thread: envelope.ThreadSummary{ThreadID: chatHark, Title: chatTitle(hostile), Participants: []string{"peer_bad", chatOp}, Group: &group, BudgetRemaining: &budget}}
	a.threads.Threads[0] = row
	a.peers = append(a.peers, chatPeer("peer_bad", hostile, hostile, hostile))
	parent := "msg_bad0"
	bad := chatMsg("msg_bad0", "peer_bad", "wake", chatAt(7, 0), hostile)
	bad.Attachments = []envelope.Attachment{{SHA256: "sha-bad", Size: 10, MediaType: hostile, Name: "..\x1b[31m.txt" + hostile}}
	job := chatMsg("msg_bad1", "peer_bad", "wake_idle", chatAt(7, 1), hostile)
	job.InReplyTo, job.Kind = &parent, "job_status"
	job.JobStatus = &envelope.JobStatus{JobID: hostile, Sequence: 1, State: hostile, Output: json.RawMessage("\"\u202e\u009b2Jpwn\"")}
	req := chatMsg("msg_bad2", "peer_bad", "hold", chatAt(7, 2), hostile)
	req.Kind = "approval_request"
	req.JobRequest = &envelope.JobRequest{Operation: hostile, JobKey: hostile}
	req.ApprovalRequest = &envelope.ApprovalRequest{ApprovalID: "apr_bad", Question: hostile, Options: []string{hostile, "no"}, ExpiresAt: chatAt(8, 0)}
	spill := chatMsg("msg_bad3", hostile, "wake", chatAt(7, 3), hostile)
	spill.Metadata = map[string]any{"vecnet.spilled": true}
	spill.Attachments = []envelope.Attachment{{SHA256: "sha-spillbad", Size: 40, MediaType: "text/plain", Name: "message.md"}}
	a.blobs["sha-spillbad"] = chatBlob{[]byte(hostile), "text/plain"}
	// A closed card: the decision is an option string the requester chose.
	closed := chatMsg("msg_bad4", "peer_bad", "hold", chatAt(6, 58), "closed one")
	closed.Kind = "approval_request"
	closed.ApprovalRequest = &envelope.ApprovalRequest{ApprovalID: "apr_shut", Question: "closed one", Options: []string{hostile, "no"}, ExpiresAt: chatAt(8, 0)}
	answer := chatMsg("msg_bad5", chatOp, "hold", chatAt(6, 59), "")
	answer.Kind = "approval_response"
	answer.ApprovalResponse = &envelope.ApprovalResponse{ApprovalID: "apr_shut", Decision: hostile}
	a.byID[chatHark] = &chatThread{
		detail: opapi.ThreadDetail{Now: envelope.FormatTime(fixedNow), Row: row,
			Files:         []opapi.ThreadFile{{SHA256: "sha-bad", Name: hostile, MediaType: hostile, MessageID: "msg_bad0", Size: 10}},
			OpenApprovals: []opapi.ThreadApproval{{ApprovalID: "apr_bad", MessageID: "msg_bad2", ExpiresAt: chatAt(8, 0), Options: []string{hostile, "no"}, AddressedToMe: true}},
		},
		messages: []envelope.Message{closed, answer, bad, job, req, spill},
		matrix: opapi.Matrix{Recipients: []string{"peer_bad", hostile}, Rows: []opapi.MatrixRow{
			{MessageID: "msg_bad0", CreatedAt: chatAt(7, 0), Sender: hostile, Cells: []opapi.MatrixCell{{Recipient: hostile, State: hostile, Display: hostile}}},
		}},
	}
	m, p := chatModel(t, a, newClock())
	check := func(t *testing.T) {
		t.Helper()
		for _, size := range [][2]int{{140, 42}, {80, 24}} {
			frame := plainAt(t, m, size[0], size[1])
			assertFrame(t, frame, size[0], size[1])
			assertChatInert(t, m.View().Content)
		}
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
	}
	t.Run("log", func(t *testing.T) {
		wantFrame(t, chatFrame(t, m), "pwn", "<img src=x onerror=alert(1)>", "answered: <img src=x")
		check(t)
	})
	t.Run("prompt", func(t *testing.T) {
		p.press("k", "a")
		check(t)
		p.press("esc", "j")
	})
	t.Run("matrix", func(t *testing.T) {
		p.press("m")
		check(t)
		p.press("esc")
	})
	t.Run("pager", func(t *testing.T) {
		p.press("e")
		p.until("body", func() bool { return a.count("Blob") == 1 })
		p.idle()
		check(t)
		p.press("esc")
	})
	t.Run("pager on a whole message", func(t *testing.T) {
		p.press("k", "k", "k", "e") // the spilled message, its job, the approval, then bad
		frame := chatFrame(t, m)
		wantFrame(t, frame, "message · ", "pwn")
		refuseFrame(t, frame, "message.md")
		if a.count("Blob") != 1 {
			t.Fatal("a whole message fetched a blob")
		}
		check(t)
		p.press("esc", "j", "j", "j")
	})
	t.Run("composer", func(t *testing.T) {
		p.press("i")
		p.send(tea.PasteMsg{Content: hostile})
		check(t)
	})
	t.Run("toast", func(t *testing.T) {
		a.fail("Send", &opclient.Error{Status: 403, Code: "scope_denied", Message: hostile})
		p.press("enter")
		waitToast(t, p, m, "scope_denied")
		check(t)
	})
}
