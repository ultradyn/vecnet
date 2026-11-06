package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
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
	if got := chatOf(m).readAsk[chatHark]; got == "msg_01away" || chatOf(m).readTo[chatHark] == "msg_01away" {
		t.Fatal("marked read while the tab is hidden")
	}
	p.press("2")
	p.until("read on return", func() bool { return len(a.reads()) == 3 })
}

// A mark that fails does not stop the unchanged thread being marked later:
// reopening it, or reloading it, asks again without a new message.
func TestChatMarkReadRetries(t *testing.T) {
	t.Run("reopen", func(t *testing.T) {
		a := chatSeed()
		a.fail("ReadThread", errors.New("hub restarting"))
		m, p := chatModel(t, a, newClock())
		p.until("first mark", func() bool { return len(a.reads()) == 1 })
		waitToast(t, p, m, "hub restarting")
		p.send(chatTickMsg{})
		if n := len(a.reads()); n != 1 {
			t.Fatalf("a failed mark retried on a tick: %d", n)
		}
		a.fail("ReadThread", nil)
		p.press("]")
		p.until("the other thread", func() bool { return m.CurrentThread != chatHark })
		p.run(m.OpenThread(chatHark))
		p.until("marked on reopen", func() bool {
			for _, id := range a.reads()[1:] {
				if id == chatHark {
					return true
				}
			}
			return false
		})
		p.until("recorded", func() bool { return chatOf(m).readTo[chatHark] != "" })
	})
	t.Run("reload", func(t *testing.T) {
		a := chatSeed()
		a.fail("ReadThread", errors.New("hub restarting"))
		m, p := chatModel(t, a, newClock())
		p.until("first mark", func() bool { return len(a.reads()) == 1 })
		waitToast(t, p, m, "hub restarting")
		p.idle()
		a.fail("ReadThread", nil)
		p.send(reloadMsg{tags: []string{threadPrefix + chatHark}})
		p.until("marked after the reload", func() bool { return len(a.reads()) == 2 })
		p.until("recorded", func() bool { return chatOf(m).readTo[chatHark] != "" })
		p.idle()
		p.send(Loaded{Key: threadPrefix + chatHark})
		if n := len(a.reads()); n != 2 {
			t.Fatalf("read again with nothing new: %d", n)
		}
	})
}

func TestSendFlow(t *testing.T) {
	t.Run("a failure after moving away names the thread that kept the text", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		release := a.hold("Send")
		p.press("i")
		typeText(p, "keep me")
		p.press("enter")
		p.until("send", func() bool { return a.count("Send") == 1 })
		p.press("esc", "]")
		if m.CurrentThread == chatHark {
			t.Fatal("] did not move")
		}
		release()
		waitToast(t, p, m, "connection refused")
		if strings.Contains(m.toast.text, "enter retries") || !strings.Contains(m.toast.text, "kept there") {
			t.Fatalf("toast %q", m.toast.text)
		}
		if d := chatOf(m).draft(chatHark); d.text != "keep me" {
			t.Fatalf("draft %q", d.text)
		}
	})
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
	t.Run("a success in Insert leaves the composer on its thread", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.sendBack = opapi.SendResult{MessageID: "msg_x", ThreadID: chatOther}
		release := a.hold("Send")
		run(t, m, p, "claw", "new", "topic")
		p.until("send", func() bool { return a.count("Send") == 1 })
		p.press("i")
		typeText(p, "do not ship ")
		release()
		waitToast(t, p, m, "sent to claw")
		typeText(p, "the build")
		if m.Mode != ModeInsert || m.CurrentThread != chatHark {
			t.Fatalf("Mode %v, thread %s", m.Mode, m.CurrentThread)
		}
		if got, other := chatOf(m).draft(chatHark).text, chatOf(m).draft(chatOther).text; got != "do not ship the build" || other != "" {
			t.Fatalf("drafts %q / %q", got, other)
		}
		p.press("enter")
		p.until("the composer send", func() bool { return len(a.sends()) == 2 })
		if r := a.sends()[1]; r.ThreadID == nil || *r.ThreadID != chatHark || r.Content.Text != "do not ship the build" {
			t.Fatalf("request %+v", r)
		}
		p.until("sent", func() bool { return m.toast.text == "sent" })
		if m.CurrentThread != chatHark {
			t.Fatalf("moved to %s while in Insert", m.CurrentThread)
		}
		p.press("esc")
		p.until("the new thread to open", func() bool { return m.CurrentThread == chatOther })
	})
	t.Run("a success behind help opens once help closes", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.sendBack = opapi.SendResult{MessageID: "msg_x", ThreadID: chatOther}
		release := a.hold("Send")
		run(t, m, p, "claw", "new", "topic")
		p.until("send", func() bool { return a.count("Send") == 1 })
		p.press("?")
		release()
		waitToast(t, p, m, "sent to claw")
		if m.CurrentThread != chatHark || !m.Help {
			t.Fatalf("thread %s, Help %v", m.CurrentThread, m.Help)
		}
		p.press("x")
		p.until("the new thread to open", func() bool { return m.CurrentThread == chatOther })
	})
	t.Run("each held line keeps its own key", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		p.press("i")
		run(t, m, p, "claw", "first")
		p.until("the first failure", func() bool { return len(chatOf(m).heldMsgs) == 1 })
		run(t, m, p, "codex", "second")
		p.until("the second failure", func() bool { return len(chatOf(m).heldMsgs) == 2 })
		p.press("esc")
		kept(t, p, m, "msg claw first", "connection refused")
		m.Mode, m.Input = ModeNormal, ""
		run(t, m, p, "claw", "first")
		p.until("retry", func() bool { return len(a.sends()) == 3 })
		sent := a.sends()
		if sent[0].IdempotencyKey != sent[2].IdempotencyKey {
			t.Fatalf("key changed: %q %q", sent[0].IdempotencyKey, sent[2].IdempotencyKey)
		}
		if sent[0].IdempotencyKey == sent[1].IdempotencyKey {
			t.Fatal("two different lines share a key")
		}
	})
	t.Run("a success between a failure and its retry keeps the key", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", errors.New("connection refused"))
		run(t, m, p, "claw", "first")
		kept(t, p, m, "msg claw first", "connection refused")
		m.Mode, m.Input = ModeNormal, ""
		a.fail("Send", nil)
		run(t, m, p, "codex", "second")
		p.until("the other send", func() bool { return len(a.sends()) == 2 })
		p.until("sent", func() bool { return strings.Contains(m.toast.text, "sent to codex") })
		run(t, m, p, "claw", "first")
		p.until("retry", func() bool { return len(a.sends()) == 3 })
		sent := a.sends()
		if sent[0].IdempotencyKey != sent[2].IdempotencyKey {
			t.Fatalf("key changed: %q %q", sent[0].IdempotencyKey, sent[2].IdempotencyKey)
		}
		p.until("sent", func() bool { return strings.Contains(m.toast.text, "sent to claw") })
		run(t, m, p, "claw", "first")
		p.until("a fresh send", func() bool { return len(a.sends()) == 4 })
		if a.sends()[3].IdempotencyKey == sent[2].IdempotencyKey {
			t.Fatal("a landed attempt kept its key, so the same words again would be dropped")
		}
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
	// The selected message's reactors: a peer with a hostile name, an unknown
	// hostile id, and an emoji outside the set, which no hub should send.
	spill.Reactions = []envelope.ReactionGroup{
		{Emoji: envelope.ReactionEmoji[5], Peers: []string{"peer_bad", hostile}},
		{Emoji: hostile, Peers: []string{hostile}},
	}
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
	t.Run("reactions", func(t *testing.T) {
		wantFrame(t, chatFrame(t, m), "[check: <img src")
		check(t)
		p.press("k")
		wantFrame(t, chatFrame(t, m), "[check 2]", "[? 1]")
		check(t)
		p.press("j")
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

// A stopped client sends nothing more from the chat: not a retained draft, not
// :msg, not an inline answer. The draft and line stay for after a restart.
func TestChatStoppedSendsNothing(t *testing.T) {
	const why = "restart with a valid profile"
	revoked := &opclient.Error{Status: 401, Code: "unauthenticated", Message: "revoked"}
	// halt stops the client the way each stop arrives: a 401 from the tail or
	// a send, or the hub saying the operator is disabled.
	halt := map[string]func(m *Model){
		"rejected": func(m *Model) { m.reject(revoked) },
		"disabled": func(m *Model) { m.onTailDone(opclient.ErrDisabled) },
	}
	for name, stop := range halt {
		want := map[string]string{"rejected": ConnRejected, "disabled": ConnDisabled}[name]
		t.Run(name+": retained draft enter", func(t *testing.T) {
			a, m, p := chatOpen(t)
			p.press("i")
			typeText(p, "kept")
			stop(m)
			if m.Conn != want {
				t.Fatalf("Conn = %q", m.Conn)
			}
			p.press("enter")
			waitToast(t, p, m, why)
			if got := a.count("Send"); got != 0 {
				t.Fatalf("Send calls %d", got)
			}
			wantFrame(t, chatFrame(t, m), "kept")
		})
		t.Run(name+": :msg", func(t *testing.T) {
			a, m, p := chatOpen(t)
			stop(m)
			p.run(m.Commands["msg"].Run(m, []string{"codex", "hello"}))
			p.until("the line to reopen", func() bool { return m.Mode == ModeCommand && m.Input == "msg codex hello" })
			wantFrame(t, chatFrame(t, m), why)
			if got := a.count("Send"); got != 0 {
				t.Fatalf("Send calls %d", got)
			}
		})
		t.Run(name+": inline answer", func(t *testing.T) {
			a, m, p := chatOpen(t)
			p.press("a")
			if chatOf(m).prompt == nil {
				t.Fatal("no prompt")
			}
			stop(m)
			p.press("1")
			waitToast(t, p, m, why)
			m.toast.text = ""
			p.press("a")
			waitToast(t, p, m, why)
			if chatOf(m).prompt != nil {
				t.Fatal("a stopped client opened the prompt")
			}
			if got := len(a.answered()); got != 0 {
				t.Fatalf("answers %d", got)
			}
		})
	}
	// The send that finds the credential rejected stops the next enter too.
	t.Run("a rejected send stops the retry", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Send", revoked)
		p.press("i")
		typeText(p, "kept")
		p.press("enter")
		p.until("rejected", func() bool { return m.Conn == ConnRejected })
		p.until("send settles", func() bool { return !chatOf(m).draft(m.CurrentThread).sending })
		p.press("enter")
		waitToast(t, p, m, why)
		if got := a.count("Send"); got != 1 {
			t.Fatalf("Send calls %d", got)
		}
	})
}

// chatAttFile writes a file to attach and returns its path.
func chatAttFile(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func chatSum(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// chatAttsOf is the open thread's chips.
func chatAttsOf(m *Model) []chatAtt { return chatOf(m).draft(m.CurrentThread).atts }

// chatAttach runs :attach path from Normal mode.
func chatAttach(p *pump, path string) {
	p.t.Helper()
	submit(p, "attach "+path)
}

// chatAttsSettled waits until the open thread has n chips, none uploading.
func chatAttsSettled(t *testing.T, p *pump, m *Model, n int) {
	t.Helper()
	p.until(fmt.Sprintf("%d settled chips", n), func() bool {
		atts := chatAttsOf(m)
		if len(atts) != n {
			return false
		}
		for _, att := range atts {
			if att.state == chatAttUploading {
				return false
			}
		}
		return true
	})
}

// chatDraftKept types text, leaves Insert, and returns, so a refusal can be
// checked against a draft that already holds something.
func chatDraftKept(p *pump, text string) {
	p.t.Helper()
	p.press("i")
	typeText(p, text)
	p.press("esc")
}

// chatRefusedAttach checks a refusal before upload: a bad toast holding want,
// no Upload call, no chip and the draft's text unchanged.
func chatRefusedAttach(t *testing.T, a *chatAPI, m *Model, want, draft string) {
	t.Helper()
	if !strings.Contains(m.toast.text, want) || !m.toast.bad {
		t.Fatalf("toast %q (bad %v), want %q", m.toast.text, m.toast.bad, want)
	}
	if got := a.count("Upload"); got != 0 {
		t.Fatalf("Upload calls %d", got)
	}
	if got := len(chatAttsOf(m)); got != 0 {
		t.Fatalf("%d chips", got)
	}
	if got := chatOf(m).draft(m.CurrentThread).text; got != draft {
		t.Fatalf("draft %q, want %q", got, draft)
	}
	frame := chatFrame(t, m)
	assertFrame(t, frame, 140, 42)
	wantFrame(t, frame, draft)
}

func TestAttachMissingFile(t *testing.T) {
	a, m, p := chatOpen(t)
	chatDraftKept(p, "keep me")
	missing := filepath.Join(t.TempDir(), "nope.txt")
	chatAttach(p, missing)
	chatRefusedAttach(t, a, m, "no such file", "keep me")
	if !strings.Contains(m.toast.text, missing) {
		t.Fatalf("toast %q does not name the path", m.toast.text)
	}

	t.Run("no open thread", func(t *testing.T) {
		a := chatSeed()
		a.threads = opapi.ThreadPage{}
		m := newTestModel(t, a, newClock())
		p := newPump(t, m)
		p.send(tea.WindowSizeMsg{Width: 140, Height: 42})
		p.run(m.Init())
		p.until("first load", func() bool { return !m.firstLoading() })
		p.idle()
		p.press("2")
		chatAttach(p, chatAttFile(t, "a.txt", "a"))
		if !strings.Contains(m.toast.text, "no thread open") || a.count("Upload") != 0 {
			t.Fatalf("toast %q, Upload calls %d", m.toast.text, a.count("Upload"))
		}
	})
}

func TestAttachDirectory(t *testing.T) {
	a, m, p := chatOpen(t)
	chatDraftKept(p, "keep me")
	dir := t.TempDir()
	chatAttach(p, dir)
	chatRefusedAttach(t, a, m, "is a directory", "keep me")
}

func TestAttachTooLarge(t *testing.T) {
	a, m, p := chatOpen(t)
	chatDraftKept(p, "keep me")
	path := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(25<<20 + 1); err != nil { // sparse: no 25 MiB written
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	chatAttach(p, path)
	chatRefusedAttach(t, a, m, "over the 25 MiB limit", "keep me")
	if got := len(a.uploaded()); got != 0 {
		t.Fatalf("the fake saw %d uploads", got)
	}

	t.Run("exactly 25 MiB is allowed", func(t *testing.T) {
		a, m, p := chatOpen(t)
		release := a.hold("Upload") // the bytes are never read
		defer release()
		path := filepath.Join(t.TempDir(), "edge.bin")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, 25<<20); err != nil {
			t.Fatal(err)
		}
		chatAttach(p, path)
		p.until("upload", func() bool { return a.count("Upload") == 1 })
		if atts := chatAttsOf(m); len(atts) != 1 || atts[0].size != 25<<20 {
			t.Fatalf("chips %+v", atts)
		}
	})
}

func TestAttachPathWithSpaceAndTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "my docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "my docs", "q3 report.txt"), []byte("quarterly"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, m, p := chatOpen(t)
	chatAttach(p, "~/my docs/q3 report.txt")
	chatAttsSettled(t, p, m, 1)
	atts := chatAttsOf(m)
	if atts[0].state != chatAttDone || atts[0].name != "q3 report.txt" || atts[0].sha != chatSum("quarterly") {
		t.Fatalf("chip %+v", atts[0])
	}
	ups := a.uploaded()
	if len(ups) != 1 || ups[0].name != "q3 report.txt" || ups[0].media != "text/plain" || string(ups[0].data) != "quarterly" || ups[0].size != 9 {
		t.Fatalf("uploads %+v", ups)
	}
	frame := chatFrame(t, m)
	assertFrame(t, frame, 140, 42)
	wantFrame(t, frame, "[+] q3 report.txt 9 B")

	// The command line splits on whitespace, so a run of spaces in a path is
	// one space: a typed double space still finds the file...
	chatAttach(p, "~/my  docs/q3   report.txt")
	chatAttsSettled(t, p, m, 2)
	// ...and a name that holds a double space cannot be reached, as the help
	// says.
	if err := os.WriteFile(filepath.Join(home, "a  b.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toast = toast{}
	chatAttach(p, "~/a  b.txt")
	if !strings.Contains(m.toast.text, "no such file") || a.count("Upload") != 2 || len(chatAttsOf(m)) != 2 {
		t.Fatalf("toast %q, uploads %d, chips %d", m.toast.text, a.count("Upload"), len(chatAttsOf(m)))
	}
	help := m.Commands["attach"].Help
	if !strings.Contains(help, "~/") || !strings.Contains(help, "spaces") {
		t.Fatalf("help %q does not explain ~/ and spaces", help)
	}
	if _, ok := m.Commands["detach"]; !ok {
		t.Fatal(":detach is not registered")
	}

	t.Run("attach alone lists the chips", func(t *testing.T) {
		m.toast = toast{}
		submit(p, "attach")
		if !strings.Contains(m.toast.text, "1 [+] q3 report.txt 9 B") || !strings.Contains(m.toast.text, "2 [+] q3 report.txt") || m.toast.bad {
			t.Fatalf("toast %q", m.toast.text)
		}
		submit(p, "detach")
		m.toast = toast{}
		submit(p, "attach")
		if !strings.Contains(m.toast.text, "no attachments") {
			t.Fatalf("toast %q", m.toast.text)
		}
	})
}

func TestAttachRetryKeepsKey(t *testing.T) {
	a, m, p := chatOpen(t)
	chatAttach(p, chatAttFile(t, "log.txt", "log line"))
	chatAttsSettled(t, p, m, 1)
	a.fail("Send", errors.New("dial tcp: connection refused"))
	p.press("i")
	typeText(p, "see attached")
	d := chatOf(m).draft(chatHark)
	settled := func(n int) {
		t.Helper()
		p.until(fmt.Sprintf("send %d", n), func() bool { return len(a.sends()) == n && !d.sending })
	}
	p.press("enter")
	settled(1)
	p.press("enter")
	settled(2)
	a.fail("Send", nil)
	p.press("enter")
	settled(3)
	waitToast(t, p, m, "sent")
	want := []envelope.Attachment{{SHA256: chatSum("log line"), Size: 8, MediaType: "text/plain", Name: "log.txt"}}
	sent := a.sends()
	for i, r := range sent {
		if r.IdempotencyKey != sent[0].IdempotencyKey {
			t.Fatalf("send %d key %q, want %q", i, r.IdempotencyKey, sent[0].IdempotencyKey)
		}
		if fmt.Sprint(r.Attachments) != fmt.Sprint(want) || r.Content.Text != "see attached" {
			t.Fatalf("send %d: %+v", i, r)
		}
	}
	if len(d.atts) != 0 || d.text != "" {
		t.Fatalf("a landed send left chips %+v, text %q", d.atts, d.text)
	}
	refuseFrame(t, chatFrame(t, m), "[+] log.txt")
	if got := a.count("Upload"); got != 1 {
		t.Fatalf("a retry uploaded again: %d uploads", got)
	}
}

func TestDetachMintsNewKey(t *testing.T) {
	a, m, p := chatOpen(t)
	chatAttach(p, chatAttFile(t, "a.txt", "aaa"))
	chatAttach(p, chatAttFile(t, "b.txt", "bbb"))
	chatAttsSettled(t, p, m, 2)
	a.fail("Send", errors.New("connection refused"))
	d := chatOf(m).draft(chatHark)
	settled := func(n int) {
		t.Helper()
		p.until(fmt.Sprintf("send %d", n), func() bool { return len(a.sends()) == n && !d.sending })
	}
	p.press("i")
	typeText(p, "two files")
	p.press("enter")
	settled(1)
	p.press("esc")

	submit(p, "detach 5")
	if !strings.Contains(m.toast.text, "no attachment 5") || len(d.atts) != 2 {
		t.Fatalf("toast %q, %d chips", m.toast.text, len(d.atts))
	}
	submit(p, "detach 1")
	if len(d.atts) != 1 || d.atts[0].name != "b.txt" {
		t.Fatalf("chips %+v", d.atts)
	}
	p.press("i", "enter")
	settled(2)
	sent := a.sends()
	if sent[1].IdempotencyKey == sent[0].IdempotencyKey {
		t.Fatal("removing a chip kept the key")
	}
	if len(sent[1].Attachments) != 1 || sent[1].Attachments[0].SHA256 != chatSum("bbb") || len(sent[0].Attachments) != 2 {
		t.Fatalf("attachments %+v then %+v", sent[0].Attachments, sent[1].Attachments)
	}
	p.press("enter") // unchanged: the same request
	settled(3)
	if a.sends()[2].IdempotencyKey != sent[1].IdempotencyKey {
		t.Fatal("an unchanged retry minted a key")
	}

	p.press("esc")
	submit(p, "detach")
	if len(d.atts) != 0 {
		t.Fatalf("detach left %+v", d.atts)
	}
	p.press("i", "enter")
	settled(4)
	last := a.sends()[3]
	if last.IdempotencyKey == sent[0].IdempotencyKey || last.IdempotencyKey == sent[1].IdempotencyKey || len(last.Attachments) != 0 || last.Content.Text != "two files" {
		t.Fatalf("after detach all: %+v", last)
	}
}

func TestDetachByIDAfterShift(t *testing.T) {
	a, m, p := chatOpen(t)
	release := a.hold("Upload")
	chatAttach(p, chatAttFile(t, "one.txt", "one"))
	chatAttach(p, chatAttFile(t, "two.txt", "two"))
	p.until("both uploads", func() bool { return a.count("Upload") == 2 })
	submit(p, "detach 1")
	if atts := chatAttsOf(m); len(atts) != 1 || atts[0].name != "two.txt" {
		t.Fatalf("chips %+v", atts)
	}
	release()
	chatAttsSettled(t, p, m, 1)
	p.idle()
	atts := chatAttsOf(m)
	if atts[0].name != "two.txt" || atts[0].state != chatAttDone || atts[0].sha != chatSum("two") || atts[0].err != "" {
		t.Fatalf("chip %+v", atts[0])
	}
	if ups := a.uploaded(); len(ups) != 1 || ups[0].name != "two.txt" {
		t.Fatalf("the detached upload went on: %+v", ups)
	}
	if m.toast.bad {
		t.Fatalf("the cancelled upload reported: %q", m.toast.text)
	}
	wantFrame(t, chatFrame(t, m), "[+] two.txt 3 B")
}

func TestAttachmentOnlySend(t *testing.T) {
	a, m, p := chatOpen(t)
	chatAttach(p, chatAttFile(t, "shot.png", "\x89PNG"))
	chatAttsSettled(t, p, m, 1)
	p.press("i", "enter")
	waitToast(t, p, m, "sent")
	sent := a.sends()
	if len(sent) != 1 {
		t.Fatalf("%d sends", len(sent))
	}
	want := []envelope.Attachment{{SHA256: chatSum("\x89PNG"), Size: 4, MediaType: "image/png", Name: "shot.png"}}
	if sent[0].Content.Text != "" || fmt.Sprint(sent[0].Attachments) != fmt.Sprint(want) {
		t.Fatalf("request %+v", sent[0])
	}
	if got := chatAttsOf(m); len(got) != 0 {
		t.Fatalf("chips left %+v", got)
	}
	p.press("enter") // nothing left to send
	p.idle()
	if got := len(a.sends()); got != 1 {
		t.Fatalf("%d sends", got)
	}
}

func TestSendRefusedWhileUploading(t *testing.T) {
	a, m, p := chatOpen(t)
	release := a.hold("Upload")
	chatAttach(p, chatAttFile(t, "big.txt", "big"))
	p.until("upload", func() bool { return a.count("Upload") == 1 })
	frame := chatFrame(t, m)
	assertFrame(t, frame, 140, 42)
	wantFrame(t, frame, "[..] big.txt 0%")
	p.press("i")
	typeText(p, "with a file")
	p.press("enter")
	waitToast(t, p, m, "uploading")
	if !m.toast.bad || a.count("Send") != 0 {
		t.Fatalf("toast %q, Send calls %d", m.toast.text, a.count("Send"))
	}
	wantFrame(t, chatFrame(t, m), "with a file")
	release()
	chatAttsSettled(t, p, m, 1)
	p.press("enter")
	waitToast(t, p, m, "sent")
	if sent := a.sends(); len(sent) != 1 || len(sent[0].Attachments) != 1 || sent[0].Content.Text != "with a file" {
		t.Fatalf("sends %+v", sent)
	}

	t.Run("a failed chip refuses too", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("Upload", errors.New("connection reset by peer"))
		chatAttach(p, chatAttFile(t, "bad.txt", "bad"))
		chatAttsSettled(t, p, m, 1)
		frame := chatFrame(t, m)
		assertFrame(t, frame, 140, 42)
		wantFrame(t, frame, "[x] bad.txt: connection reset by peer")
		p.press("i")
		typeText(p, "kept")
		p.press("enter")
		waitToast(t, p, m, ":detach 1")
		if a.count("Send") != 0 {
			t.Fatal("sent with a failed chip")
		}
		p.press("esc")
		submit(p, "detach 1")
		p.press("i", "enter")
		waitToast(t, p, m, "sent")
		if sent := a.sends(); len(sent) != 1 || len(sent[0].Attachments) != 0 || sent[0].Content.Text != "kept" {
			t.Fatalf("sends %+v", sent)
		}
	})
}

func TestSendRefusedSpillSlot(t *testing.T) {
	const slot = "Remove one attachment or shorten the text: the long text needs an attachment slot."
	a, m, p := chatOpen(t)
	dir := t.TempDir()
	for i := range 16 {
		path := filepath.Join(dir, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprint(i)), 0o600); err != nil {
			t.Fatal(err)
		}
		chatAttach(p, path)
	}
	chatAttsSettled(t, p, m, 16)
	frame := chatFrame(t, m)
	assertFrame(t, frame, 140, 42)
	wantFrame(t, frame, "[+] f00.txt", "more")

	// A 17th is refused before upload, the draft intact.
	chatAttach(p, chatAttFile(t, "f16.txt", "x"))
	if !strings.Contains(m.toast.text, "at most 16") || a.count("Upload") != 16 || len(chatAttsOf(m)) != 16 {
		t.Fatalf("toast %q, uploads %d, chips %d", m.toast.text, a.count("Upload"), len(chatAttsOf(m)))
	}

	long := strings.Repeat("a", 4097)
	p.press("i")
	p.send(tea.PasteMsg{Content: long})
	p.press("enter")
	waitToast(t, p, m, slot)
	if a.count("Send") != 0 || chatOf(m).draft(chatHark).text != long || len(chatAttsOf(m)) != 16 {
		t.Fatal("the spill refusal sent or changed the draft")
	}
	// Fifteen leave the slot free.
	p.press("esc")
	submit(p, "detach 16")
	p.press("i", "enter")
	waitToast(t, p, m, "sent")
	if sent := a.sends(); len(sent) != 1 || len(sent[0].Attachments) != 15 {
		t.Fatalf("sends %d", len(sent))
	}

	t.Run("the hub's 413 says the same", func(t *testing.T) {
		a, m, p := chatOpen(t)
		chatAttach(p, chatAttFile(t, "one.txt", "1"))
		chatAttsSettled(t, p, m, 1)
		a.fail("Send", &opclient.Error{Status: 413, Code: "payload_too_large", Message: "too many attachments", Details: map[string]any{"field": "attachments", "limit": float64(15)}})
		p.press("i")
		typeText(p, "kept")
		p.press("enter")
		waitToast(t, p, m, slot)
		wantFrame(t, chatFrame(t, m), "kept", "[+] one.txt")
	})
}

func TestAttachQuotaExceededShowsRetry(t *testing.T) {
	a, m, p := chatOpen(t)
	a.fail("Upload", &opclient.Error{Status: 429, Code: "upload_quota_exceeded", Message: "daily upload quota exceeded", Details: map[string]any{"limit": float64(500 << 20), "retry_after_seconds": float64(3600)}})
	chatDraftKept(p, "keep me")
	chatAttach(p, chatAttFile(t, "q.png", "png"))
	chatAttsSettled(t, p, m, 1)
	att := chatAttsOf(m)[0]
	if att.state != chatAttFailed {
		t.Fatalf("chip %+v", att)
	}
	// 07:41 on the test clock, in UTC, plus an hour.
	frame := chatFrame(t, m)
	assertFrame(t, frame, 140, 42)
	wantFrame(t, frame, "[x] q.png: upload_quota_exceeded: daily upload quota exceeded (retry after 3600s, at 08:41)", "keep me")
	if !m.toast.bad || !strings.Contains(m.toast.text, "08:41") {
		t.Fatalf("toast %q", m.toast.text)
	}
}

func TestAttachTooLargeFromHub(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details map[string]any
		want    string
	}{
		{"with the limit", map[string]any{"limit": float64(1 << 20)}, "[x] big.bin: too large, the hub's limit is 1.0 MiB"},
		{"without it", map[string]any{}, "[x] big.bin: too large, the hub's limit is 25.0 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m, p := chatOpen(t)
			a.fail("Upload", &opclient.Error{Status: 413, Code: "blob_too_large", Message: "blob exceeds the per-blob cap", Details: tc.details})
			chatDraftKept(p, "keep me")
			chatAttach(p, chatAttFile(t, "big.bin", "bytes"))
			chatAttsSettled(t, p, m, 1)
			if chatAttsOf(m)[0].state != chatAttFailed {
				t.Fatalf("chip %+v", chatAttsOf(m)[0])
			}
			wantFrame(t, chatFrame(t, m), tc.want, "keep me")
		})
	}
}

// A chip replaced by the same bytes under another name (and so another media
// type) is a different request body: after a lost answer the next send must
// mint a new key, or the hub refuses it as 409 idempotency_conflict.
func TestReattachSameBytesNewNameMintsNewKey(t *testing.T) {
	a, m, p := chatOpen(t)
	chatAttach(p, chatAttFile(t, "a.txt", "same"))
	chatAttsSettled(t, p, m, 1)
	a.fail("Send", errors.New("connection refused"))
	d := chatOf(m).draft(chatHark)
	settled := func(n int) {
		t.Helper()
		p.until(fmt.Sprintf("send %d", n), func() bool { return len(a.sends()) == n && !d.sending })
	}
	p.press("i", "enter")
	settled(1)
	p.press("esc")
	submit(p, "detach 1")
	chatAttach(p, chatAttFile(t, "b.txt", "same"))
	chatAttsSettled(t, p, m, 1)
	p.press("i", "enter")
	settled(2)
	p.press("esc")
	submit(p, "detach 1")
	chatAttach(p, chatAttFile(t, "b.bin", "same"))
	chatAttsSettled(t, p, m, 1)
	p.press("i", "enter")
	settled(3)
	p.press("enter") // unchanged: the same request
	settled(4)
	sent := a.sends()
	if sent[0].Attachments[0].SHA256 != sent[2].Attachments[0].SHA256 || sent[1].Attachments[0].Name != "b.txt" || sent[2].Attachments[0].MediaType != "application/octet-stream" {
		t.Fatalf("attachments %+v, %+v, %+v", sent[0].Attachments, sent[1].Attachments, sent[2].Attachments)
	}
	if sent[1].IdempotencyKey == sent[0].IdempotencyKey {
		t.Fatal("a new name on the same bytes kept the key")
	}
	if sent[2].IdempotencyKey == sent[1].IdempotencyKey {
		t.Fatal("a new media type on the same bytes kept the key")
	}
	if sent[3].IdempotencyKey != sent[2].IdempotencyKey {
		t.Fatal("an unchanged retry minted a new key")
	}
}

// A hub or proxy error is untrusted: the failed chip shows it cleaned, with no
// escape sequence, bidi control or line break reaching the frame.
func TestAttachHostileUploadErrorCleaned(t *testing.T) {
	a, m, p := chatOpen(t)
	hostile := "evil\x1b]8;;http://x.example/\x07link\x1b]8;;\x07 \x1b[31mred\x1b[0m ‮desrever‬\nsecond line"
	a.fail("Upload", &opclient.Error{Status: 500, Code: "boom\x1b[2J", Message: hostile})
	chatAttach(p, chatAttFile(t, "x.bin", "bytes"))
	chatAttsSettled(t, p, m, 1)
	att := chatAttsOf(m)[0]
	if att.state != chatAttFailed {
		t.Fatalf("chip %+v", att)
	}
	for _, bad := range []string{"\x1b", "\x07", "‮", "‬", "\n"} {
		if strings.Contains(att.err, bad) || strings.Contains(att.chip(chatAttNameMax), bad) {
			t.Fatalf("chip error keeps %q: %q", bad, att.err)
		}
	}
	if !strings.Contains(att.err, "evil") || !strings.Contains(att.err, "second line") {
		t.Fatalf("chip error lost its text: %q", att.err)
	}
	wantFrame(t, chatFrame(t, m), "[x] x.bin: boom")
}

// React answers as the hub would: it changes the operator's reaction on the
// stored message and returns the whole aggregate in the fixed order.
func (a *chatAPI) React(ctx context.Context, r envelope.ReactionRequest) (opapi.ReactionResult, error) {
	if _, err := a.fakeAPI.React(ctx, r); err != nil {
		return opapi.ReactionResult{}, err
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	out := opapi.ReactionResult{MessageID: r.MessageID, Emoji: r.Emoji, Op: r.Op, Changed: true, Reactions: []envelope.ReactionGroup{}, Superseded: []string{}}
	for id, th := range a.byID {
		for i := range th.messages {
			msg := &th.messages[i]
			if msg.MessageID != r.MessageID {
				continue
			}
			out.ThreadID = id
			msg.Reactions = chatToggled(msg.Reactions, r.Emoji, chatOp, r.Op == "add")
			out.Reactions = append(out.Reactions, msg.Reactions...)
		}
	}
	return out, nil
}

// chatToggled adds or removes peer under emoji, keeping the fixed order.
func chatToggled(groups []envelope.ReactionGroup, emoji, peer string, add bool) []envelope.ReactionGroup {
	var out []envelope.ReactionGroup
	found := false
	for _, g := range groups {
		peers := append([]string(nil), g.Peers...)
		if g.Emoji == emoji {
			found = true
			if i := slices.Index(peers, peer); add && i < 0 {
				peers = append(peers, peer)
			} else if !add && i >= 0 {
				peers = slices.Delete(peers, i, i+1)
			}
		}
		if len(peers) > 0 {
			out = append(out, envelope.ReactionGroup{Emoji: g.Emoji, Peers: peers})
		}
	}
	if !found && add {
		out = append(out, envelope.ReactionGroup{Emoji: emoji, Peers: []string{peer}})
		slices.SortStableFunc(out, func(x, y envelope.ReactionGroup) int {
			return envelope.ReactionIndex(x.Emoji) - envelope.ReactionIndex(y.Emoji)
		})
	}
	return out
}

var (
	rxUp    = envelope.ReactionEmoji[0]
	rxDown  = envelope.ReactionEmoji[1]
	rxEyes  = envelope.ReactionEmoji[4]
	rxCheck = envelope.ReactionEmoji[5]
	rxQ     = envelope.ReactionEmoji[6]
	rxBang  = envelope.ReactionEmoji[7]
)

// chatReacted is chatSeed with reactions on the root message: codex and
// sleuth gave +1, and the operator's own peer gave check.
func chatReacted() *chatAPI {
	a := chatSeed()
	a.byID[chatHark].messages[0].Reactions = []envelope.ReactionGroup{
		{Emoji: rxUp, Peers: []string{chatCodex, chatSleuth}},
		{Emoji: rxCheck, Peers: []string{chatOp}},
	}
	return a
}

// chatLineOf is the index of the first frame line holding s, or -1.
func chatLineOf(frame, s string) int {
	return slices.IndexFunc(strings.Split(frame, "\n"), func(line string) bool { return strings.Contains(line, s) })
}

// chatSelect moves the selection onto the message with id.
func chatSelect(t *testing.T, p *pump, m *Model, id string) {
	t.Helper()
	for range 20 {
		st := m.Data.Thread[m.CurrentThread]
		if msg := chatOf(m).selected(st); msg != nil && msg.MessageID == id {
			return
		}
		p.press("k")
	}
	t.Fatalf("could not select %s", id)
}

// react runs :react with args, as the command line would.
func react(t *testing.T, p *pump, m *Model, args ...string) {
	t.Helper()
	cmd, ok := m.Commands["react"]
	if !ok {
		t.Fatal(":react is not registered")
	}
	p.run(cmd.Run(m, args))
}

func TestReactionChipsRow(t *testing.T) {
	a := chatReacted()
	m, _ := chatModel(t, a, newClock())
	frame := chatFrame(t, m)
	wantFrame(t, frame, "[+1 2] [check 1*]")
	lines := strings.Split(frame, "\n")
	chips := chatLineOf(frame, "[+1 2] [check 1*]")
	// The root message's receipts row (grok processed, max read) follows it.
	if chips < 0 || chips+1 >= len(lines) || !strings.Contains(lines[chips+1], "grok✓✓") || !strings.Contains(lines[chips+1], "max●") {
		t.Fatalf("the chips row is not right above the root's receipts:\n%s", frame)
	}
	// The log is the focused pane, so its border is heavy.
	if !strings.Contains(lines[chips], "┃"+chatIndent+"[+1 2]") && !strings.Contains(lines[chips], " "+chatIndent+"[+1 2]") {
		t.Fatalf("chips row is not indented by chatIndent: %q", lines[chips])
	}
	refuseFrame(t, frame, "[+1: ", "[check: ")
	for _, msg := range a.byID[chatHark].messages[1:] {
		if len(msg.Reactions) != 0 {
			t.Fatal("seed gave another message reactions")
		}
	}
	if strings.Count(frame, "[+1 ") != 1 {
		t.Fatalf("chips drawn for a message without reactions:\n%s", frame)
	}
}

func TestReactionChipsNamesWhenSelected(t *testing.T) {
	a := chatReacted()
	m, p := chatModel(t, a, newClock())
	chatSelect(t, p, m, "msg_01root")
	frame := chatFrame(t, m)
	// Names are l.names.short: codex-hark is codex, pag-hark-sleuth is pag,
	// and the operator's own peer is you.
	wantFrame(t, frame, "[+1: codex, pag] [check: you]")
	refuseFrame(t, frame, "[+1 2]", "[check 1*]")
}

func TestReactCommandToggles(t *testing.T) {
	a, m, p := chatOpen(t)
	react(t, p, m, "check")
	waitToast(t, p, m, "reacted check")
	p.until("the chip", func() bool { return strings.Contains(chatFrame(t, m), "[check: you]") })
	react(t, p, m, "check")
	waitToast(t, p, m, "removed check")
	p.until("the chip to go", func() bool { return !strings.Contains(chatFrame(t, m), "[check") })
	got := a.reactions()
	if len(got) != 2 {
		t.Fatalf("%d reactions sent, want 2: %+v", len(got), got)
	}
	for i, op := range []string{"add", "remove"} {
		if got[i].Op != op || got[i].Emoji != rxCheck || got[i].MessageID != "msg_01apr" || got[i].As != "" {
			t.Fatalf("reaction %d = %+v, want %s %s on msg_01apr", i, got[i], op, rxCheck)
		}
	}
	if got[0].Priority != "wake_idle" {
		t.Fatalf("add priority %q", got[0].Priority)
	}
}

func TestReactCommandDefaults(t *testing.T) {
	a, m, p := chatOpen(t)
	steps := []struct {
		args       []string
		emoji, pri string
	}{
		{[]string{"check"}, rxCheck, "wake_idle"},
		{[]string{"eyes"}, rxEyes, "hold"},
		{[]string{"laugh", "--wake"}, envelope.ReactionEmoji[3], "wake"},
		{[]string{"--hold", "bang"}, rxBang, "hold"},
		{[]string{"--wake-idle", "+1"}, rxUp, "wake_idle"},
	}
	for i, step := range steps {
		react(t, p, m, step.args...)
		p.until("the reaction", func() bool { return len(a.reactions()) == i+1 })
		waitToast(t, p, m, "reacted "+envelope.ReactionName(step.emoji))
		got := a.reactions()[i]
		if got.Op != "add" || got.Emoji != step.emoji || got.Priority != step.pri {
			t.Fatalf("%v sent %+v, want add %s %s", step.args, got, step.emoji, step.pri)
		}
	}
}

func TestReactCommandByNumber(t *testing.T) {
	a, m, p := chatOpen(t)
	// Newest first, the peer messages are the approval, the fix (the hub's
	// expired line between them does not count), the job, ...
	react(t, p, m, "2", "+1")
	waitToast(t, p, m, "reacted +1")
	chatSelect(t, p, m, "msg_01job")
	react(t, p, m, "+1")
	p.until("second", func() bool { return len(a.reactions()) == 2 })
	waitToast(t, p, m, "reacted +1")
	react(t, p, m, "-1")
	p.until("third", func() bool { return len(a.reactions()) == 3 })
	waitToast(t, p, m, "reacted -1")
	want := []struct{ id, emoji string }{{"msg_01fix", rxUp}, {"msg_01job", rxUp}, {"msg_01job", rxDown}}
	for i, got := range a.reactions() {
		if got.MessageID != want[i].id || got.Emoji != want[i].emoji || got.Op != "add" {
			t.Fatalf("reaction %d = %+v, want %s on %s", i, got, want[i].emoji, want[i].id)
		}
	}
}

func TestReactCommandRefusals(t *testing.T) {
	kept := func(t *testing.T, p *pump, m *Model, input string, reason ...string) {
		t.Helper()
		p.until("the line to reopen", func() bool { return m.Mode == ModeCommand && m.Input == input })
		frame := chatFrame(t, m)
		wantFrame(t, frame, ":"+input)
		wantFrame(t, frame, reason...)
	}
	t.Run("no thread", func(t *testing.T) {
		a := chatSeed()
		a.threads = opapi.ThreadPage{}
		m := newTestModel(t, a, newClock())
		p := newPump(t, m)
		p.send(tea.WindowSizeMsg{Width: 140, Height: 42})
		p.run(m.Init())
		p.until("first load", func() bool { return !m.firstLoading() })
		p.press("2")
		react(t, p, m, "check")
		kept(t, p, m, "react check", "no open thread")
		if a.count("React") != 0 {
			t.Fatal("reacted with no thread")
		}
	})
	t.Run("no message", func(t *testing.T) {
		a := chatSeed()
		a.byID[chatHark].messages = nil
		m, p := chatModel(t, a, newClock())
		react(t, p, m, "check")
		kept(t, p, m, "react check", "no message to react to")
		if a.count("React") != 0 {
			t.Fatal("reacted in an empty thread")
		}
	})
	cases := []struct {
		name   string
		args   []string
		reason []string
	}{
		{"n out of range", []string{"7", "+1"}, []string{"no message 7: the thread has 6"}},
		{"unknown name", []string{"smile"}, []string{"unknown reaction smile", "+1", "heart", "check", "bang"}},
		{"unknown name by number", []string{"1", "smile"}, []string{"unknown reaction smile"}},
		{"two flags", []string{"check", "--wake", "--hold"}, []string{"one priority flag at most"}},
		{"no emoji", nil, []string{"usage: react"}},
		{"too many", []string{"1", "2", "check"}, []string{"usage: react"}},
		{"n not a number", []string{"x", "check"}, []string{"usage: react"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a, m, p := chatOpen(t)
			react(t, p, m, tt.args...)
			kept(t, p, m, strings.TrimSpace("react "+strings.Join(tt.args, " ")), tt.reason...)
			if a.count("React") != 0 {
				t.Fatalf("reacted despite %s", tt.name)
			}
		})
	}
	t.Run("the toast lists the set", func(t *testing.T) {
		_, m, p := chatOpen(t)
		react(t, p, m, "smile")
		waitToast(t, p, m, "use one of +1 -1 heart laugh eyes check q bang")
		for _, emoji := range envelope.ReactionEmoji {
			if strings.Contains(m.toast.text, emoji) {
				t.Fatalf("the refusal prints %s: %q", emoji, m.toast.text)
			}
		}
	})
}

func TestReactHubErrorToast(t *testing.T) {
	t.Run("not_replyable", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("React", &opclient.Error{Status: 422, Code: "not_replyable", Message: "reaction messages cannot be reacted to"})
		react(t, p, m, "check")
		waitToast(t, p, m, "not_replyable")
		if !m.toast.bad {
			t.Fatal("the hub's refusal is not a bad toast")
		}
		refuseFrame(t, chatFrame(t, m), "[check")
	})
	t.Run("rate_limited", func(t *testing.T) {
		a, m, p := chatOpen(t)
		a.fail("React", &opclient.Error{Status: 429, Code: "rate_limited", Message: "too many sends", Details: map[string]any{"retry_after_seconds": float64(12)}})
		react(t, p, m, "check")
		waitToast(t, p, m, "rate_limited")
		waitToast(t, p, m, "retry after 12s")
	})
}

func TestChatReactionsGolden(t *testing.T) {
	a := chatSeed()
	msgs := a.byID[chatHark].messages
	msgs[4].Reactions = []envelope.ReactionGroup{{Emoji: rxUp, Peers: []string{chatSleuth, chatGrok}}, {Emoji: rxCheck, Peers: []string{chatOp}}}
	msgs[6].Reactions = []envelope.ReactionGroup{{Emoji: rxEyes, Peers: []string{chatCodex}}, {Emoji: rxQ, Peers: []string{chatOp, chatSleuth}}}
	m, _ := chatModel(t, a, newClock())
	frame := chatGolden(t, m, "reactions-80x24", 80, 24)
	assertFrame(t, frame, 80, 24)
	wantFrame(t, frame, "[eyes: codex] [q: you, pag]", ":react")
}

// A peer's reaction to a message the client already holds arrives as a
// reaction Event; the thread's reload must show it, though the message is
// older than the newest one the client holds.
// rxTail opens the chat with a tail the test feeds: deliver hands the model
// one batch and returns once it is acked and the reloads have settled.
func rxTail(t *testing.T) (*chatAPI, *Model, *pump, func(opclient.Batch)) {
	t.Helper()
	a := chatSeed()
	next := make(chan opclient.Batch)
	acked := make(chan error, 1)
	a.tail = func(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error {
		status("live")
		for {
			select {
			case b := <-next:
				acked <- handle(b)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	m, p := chatModel(t, a, newClock())
	deliver := func(b opclient.Batch) {
		t.Helper()
		go func() { next <- b }()
		p.until("the batch to be handled", func() bool {
			select {
			case err := <-acked:
				if err != nil {
					t.Fatal(err)
				}
				return true
			default:
				return false
			}
		})
		p.idle()
	}
	return a, m, p, deliver
}

// rxSetReactions changes a held message's reactions on the fake hub only.
func rxSetReactions(a *chatAPI, i int, groups []envelope.ReactionGroup) {
	a.cmu.Lock()
	a.byID[chatHark].messages[i].Reactions = groups
	a.cmu.Unlock()
}

func TestReactionEventRefreshesHeldMessage(t *testing.T) {
	a, m, _, deliver := rxTail(t)
	refuseFrame(t, chatFrame(t, m), "[+1 ")
	rxSetReactions(a, 4, []envelope.ReactionGroup{{Emoji: rxUp, Peers: []string{chatCodex}}})
	data, err := json.Marshal(envelope.ReactionEvent{MessageID: "msg_01fix", ThreadID: chatHark, PeerID: chatCodex, Emoji: rxUp, Op: "add", At: chatAt(7, 40)})
	if err != nil {
		t.Fatal(err)
	}
	deliver(opclient.Batch{Cursor: "e9", Events: []envelope.Event{{Cursor: "e9", Type: "reaction", Data: data}}})
	frame := chatFrame(t, m)
	wantFrame(t, frame, "[+1 1]")
	if n := len(m.Data.Thread[chatHark].Messages); n != len(a.byID[chatHark].messages) {
		t.Fatalf("the refetch holds %d messages, want %d", n, len(a.byID[chatHark].messages))
	}
}

// An operator who is not a participant gets operator_view {reason:"reaction"}
// instead of the reaction Event, with no message id (PROTOCOL §17.3): the
// open thread's held messages are all fetched again.
func TestOperatorViewReactionRefreshesHeldMessage(t *testing.T) {
	a, m, _, deliver := rxTail(t)
	rxSetReactions(a, 0, []envelope.ReactionGroup{{Emoji: rxUp, Peers: []string{chatCodex}}})
	data := json.RawMessage(`{"reason":"reaction","thread_id":"` + chatHark + `"}`)
	deliver(opclient.Batch{Cursor: "e9", Events: []envelope.Event{{Cursor: "e9", Type: "operator_view", Data: data}}})
	rxWantHeld(t, m, 0, rxUp)
	if n := len(m.Data.Thread[chatHark].Messages); n != len(a.byID[chatHark].messages) {
		t.Fatalf("the refetch holds %d messages, want %d", n, len(a.byID[chatHark].messages))
	}
}

// A purged cursor resets the tail with no per-message Events, so every held
// thread is fetched again from its first message (operator UI Review Focus 4).
func TestCursorResetRefreshesHeldReactions(t *testing.T) {
	a, m, _, deliver := rxTail(t)
	rxSetReactions(a, 0, []envelope.ReactionGroup{{Emoji: rxUp, Peers: []string{chatCodex}}})
	deliver(opclient.Batch{Cursor: "e9", Reset: true})
	rxWantHeld(t, m, 0, rxUp)
}

// rxWantHeld checks the model holds message i of the hark thread with one
// reaction group of emoji (the root is scrolled out of the frame).
func rxWantHeld(t *testing.T, m *Model, i int, emoji string) {
	t.Helper()
	got := m.Data.Thread[chatHark].Messages[i].Reactions
	if len(got) != 1 || got[0].Emoji != emoji {
		t.Fatalf("held message %d reactions = %+v, want one %s group", i, got, emoji)
	}
}

// A :react response held back until after the tail loaded a newer aggregate
// must not roll the message back to the response's older snapshot.
func TestDelayedReactResponseKeepsNewerReactions(t *testing.T) {
	a, m, p, deliver := rxTail(t)
	release := a.hold("React")
	react(t, p, m, "check")
	p.until("the request", func() bool { return a.count("React") == 1 })
	rxSetReactions(a, 6, []envelope.ReactionGroup{{Emoji: rxCheck, Peers: []string{chatOp, chatCodex}}})
	data, err := json.Marshal(envelope.ReactionEvent{MessageID: "msg_01apr", ThreadID: chatHark, PeerID: chatCodex, Emoji: rxCheck, Op: "add", At: chatAt(7, 40)})
	if err != nil {
		t.Fatal(err)
	}
	deliver(opclient.Batch{Cursor: "e9", Events: []envelope.Event{{Cursor: "e9", Type: "reaction", Data: data}}})
	wantFrame(t, chatFrame(t, m), "[check: you, codex]")
	release()
	waitToast(t, p, m, "reacted check")
	p.idle()
	wantFrame(t, chatFrame(t, m), "[check: you, codex]")
}

// A CLI send with the operator credential carries the hub-set
// metadata["vecnet.client"]; the log names the terminal beside the sender,
// cleaned like any other hub-relayed string.
func TestChatOperatorCLILabel(t *testing.T) {
	a := chatSeed()
	cli := chatMsg("msg_01cli", chatOp, "wake", chatAt(7, 6), "sent from my terminal")
	cli.Metadata = map[string]any{envelope.ClientMetadataKey: map[string]any{
		"kind": "operator-cli", "host": "x-tall", "ssh": true,
		"label": "Operator on x-tall CLI via ssh",
	}}
	hostile := chatMsg("msg_01cli2", chatOp, "hold", chatAt(7, 7), "second")
	hostile.Metadata = map[string]any{envelope.ClientMetadataKey: map[string]any{"kind": "operator-cli", "label": "Operator on \x1b[31mevil\nhost CLI"}}
	other := chatMsg("msg_01cli3", chatOp, "hold", chatAt(7, 8), "third")
	other.Metadata = map[string]any{envelope.ClientMetadataKey: map[string]any{"kind": "web", "label": "not a CLI"}}
	th := a.byID[chatHark]
	th.messages = append(th.messages, cli, hostile, other)
	m, _ := chatModel(t, a, newClock())
	frame := chatFrame(t, m)
	wantFrame(t, frame, "<max> [Operator on x-tall CLI via ssh] sent from my terminal", "[Operator on evil host CLI] second")
	refuseFrame(t, frame, "not a CLI")
	assertChatInert(t, m.View().Content)
	for _, size := range [][2]int{{140, 42}, {80, 24}} {
		assertFrame(t, plainAt(t, m, size[0], size[1]), size[0], size[1])
	}
}

// The chat title's budget takes the thread budget map, as the dash does: 0 is
// warn, otherwise muted (design §5.5).
func TestChatTitleBudgetRole(t *testing.T) {
	th := theme.New(theme.TrueColor, true)
	for _, tc := range []struct {
		budget int
		role   theme.Role
	}{{494, theme.Muted}, {0, theme.Warn}} {
		a := chatSeed()
		*a.threads.Threads[0].Thread.BudgetRemaining = tc.budget
		m, _ := chatModel(t, a, newClock())
		title := strings.Split(frameOf(m, th), "\n")[1]
		if want := th.Fg(tc.role).Render(fmt.Sprint(tc.budget)); !strings.Contains(title, want) {
			t.Errorf("budget %d: %q not in %q", tc.budget, want, title)
		}
		if plain := ansi.Strip(title); !strings.Contains(plain, fmt.Sprintf("· budget %d ━", tc.budget)) {
			t.Errorf("budget %d: plain %q", tc.budget, plain)
		}
	}
}
