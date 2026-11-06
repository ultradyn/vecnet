package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

const (
	// chatPoll is how often the screen looks at the tab: a tab switch reaches
	// no screen, and the thread it reveals still has to open and mark read.
	chatPoll = 250 * time.Millisecond

	chatExpandMax = 1 << 20  // the most spilled text e will show
	chatBlobMax   = 25 << 20 // §2.7's cap on one blob, for s
	chatSpillAt   = 4096     // bytes a message holds before the hub spills it
	chatSaveTries = 1000

	chatSpillMeta = "vecnet.spilled"
	chatSpillName = "message.md"

	chatAttMax      = 16  // attachments the hub takes on one send
	chatAttNameMax  = 255 // characters an attachment's name keeps
	chatChipNameMax = 32  // cells a chip gives a name
	// chatSpillSlot is the refusal when a long text, which the hub spills into
	// one more attachment, would make a 17th (internal/hub/spill.go).
	chatSpillSlot = "Remove one attachment or shorten the text: the long text needs an attachment slot."

	chatIndent    = "       " // lines under a message start below its text
	chatHold      = " "       // keeps the wrap off the styled head of a line
	chatPreview   = 3         // wrapped lines a spilled preview shows
	chatMatrixDay = 6         // width of the matrix's time column
	chatMatrixWho = 15        // width of its sender column
	chatMatrixBox = 14        // width of one recipient's column
	chatHintsWide = 140       // width at which every word of the hints still fits
)

var (
	chatPrios = []string{"wake", "wake_idle", "hold"}
	colorCyan = lipgloss.Color("#22d3ee")
	colorPick = lipgloss.Color("#1e2a4a")
	styleCyan = lipgloss.NewStyle().Foreground(colorCyan)
	// styleAccepted is accepted's violet, as on the web and in a-chat.png, so
	// accepted and processed differ by colour as well as by glyph.
	styleAccepted = lipgloss.NewStyle().Foreground(lipgloss.Color("#818cf8"))
	styleSender   = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#818cf8")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#34d399")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#a3e635")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#fb923c")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#c084fc")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22d3ee")).Bold(true),
	}
)

type chatOverlay int

const (
	chatNoOverlay chatOverlay = iota
	chatMatrix
	chatPager
)

// chatAttempt is what was last offered to the hub. While the composer still
// holds exactly this, a retry reuses the key: the hub replays a send it already
// took, and a changed body under one key would be idempotency_conflict.
// shas is chatShas of the chips: each attachment's wire fields, in order, so a
// changed attachment list is a new request too (PROTOCOL §5.3).
type chatAttempt struct{ text, prio, reply, shas string }

// chatDraft is one thread's composer.
type chatDraft struct {
	text, reply string
	atts        []chatAtt
	attempt     chatAttempt
	key         string // the key of attempt; empty when nothing is awaiting retry
	sending     bool
}

// chatAttState is where one chip's upload stands.
type chatAttState int

const (
	chatAttUploading chatAttState = iota
	chatAttDone
	chatAttFailed
)

// chatAtt is one file on a draft, shown as a chip. An upload's completion finds
// its chip by id, never by place: :detach moves the chips after it.
type chatAtt struct {
	id     int
	name   string // cleaned, at most chatAttNameMax characters
	size   int64
	sha    string // set once the hub has the bytes
	media  string
	state  chatAttState
	err    string             // why a failed upload failed
	cancel context.CancelFunc // stops the upload
	read   *atomic.Int64      // bytes the upload has read, for the progress the tick draws
}

// chatCounter counts what an upload reads, from the command's goroutine.
type chatCounter struct {
	r io.Reader
	n *atomic.Int64
}

func (c *chatCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// chatHeldMsg is a :msg or :react that did not go, waiting for the command
// line.
type chatHeldMsg struct {
	cmd  string // the command's name, which the line starts with again
	args []string
	why  string // the reason to show with the line
}

// chatNewAttempt is chatAttempt for :msg, which has no composer to live in.
type chatNewAttempt struct{ peer, text, prio string }

// chatAnswerState is an approval this client has acted on, kept so the key is
// refused from the moment it lands rather than when the reload does.
type chatAnswerState int

const (
	chatAnswering chatAnswerState = iota + 1
	chatAnswered
	chatClosed
)

// chatReader is the overlay that shows one message's full text.
type chatReader struct {
	message, title, body string
	loading              bool
	lines                []string
	width, top, rows     int
}

type (
	chatTickMsg struct{}
	chatSentMsg struct {
		thread, text, shas string
		result             opapi.SendResult
		err                error
	}
	chatUploadedMsg struct {
		thread string
		id     int // the chip, which may have moved or gone since
		att    envelope.Attachment
		err    error
	}
	chatStartedMsg struct {
		args    []string
		peer    string
		attempt chatNewAttempt
		result  opapi.SendResult
		err     error
	}
	chatRefusedMsg struct {
		cmd  string // "" is msg
		args []string
		why  string
	}
	chatReactedMsg struct {
		thread, message, emoji, op string
		result                     opapi.ReactionResult
		err                        error
	}
	chatAnsweredMsg struct {
		approval opapi.OpenApproval
		option   int
		err      error
	}
	chatBlobMsg struct {
		message string
		data    []byte
		media   string
		err     error
	}
	chatSavedMsg struct {
		name string
		err  error
	}
	chatReadMsg struct {
		thread string
		last   string // the newest message when the mark was asked for
		marked int
		err    error
	}
)

// chatScreen is the chat tab: thread list, log, who-and-files, composer.
type chatScreen struct {
	ticking, registered, insert, narrow bool

	thread  string // the thread the selection below belongs to
	sel     string // selected message id, unless follow
	follow  bool   // the selection stays on the newest message
	top     int    // first message drawn
	fileIdx int    // which of the selected message's files s saves

	// othersOpen is the Other Threads section. It lasts for this session only.
	othersOpen bool
	otherLimit int
	listTop    int
	listH      int
	listN      int
	followList bool
	whoFrom    int

	drafts map[string]*chatDraft
	readTo map[string]string // newest message each thread was marked read at, once the hub said so
	// readAsk is the mark in flight per thread, and readFailed the newest
	// message of a mark that failed. A failed mark waits for the thread to be
	// opened or loaded again, not the next tick, so a hub that keeps failing
	// sees one POST per reload rather than a storm.
	readAsk, readFailed map[string]string
	answers             map[string]chatAnswerState
	prompt              *opapi.OpenApproval // the approval waiting for its digit

	overlay          chatOverlay
	matrixX, matrixY int
	reader           chatReader
	newKeys          map[chatNewAttempt]string // :msg keys awaiting a retry, until that attempt lands
	openNew          string                    // a :msg thread to open once the line is free
	msgErr           string                    // why the :msg back on the command line failed
	heldMsgs         []chatHeldMsg             // oldest first
	nextAtt          int                       // the last chip id given out
}

func newChatScreen() Screen {
	return &chatScreen{follow: true, othersOpen: true, otherLimit: otherPage, followList: true, drafts: map[string]*chatDraft{}, readTo: map[string]string{}, readAsk: map[string]string{}, readFailed: map[string]string{}, answers: map[string]chatAnswerState{}, newKeys: map[chatNewAttempt]string{}}
}

func (s *chatScreen) Title() string { return "chat" }

func (s *chatScreen) Hints() string {
	switch {
	case s.prompt != nil:
		return "digit answers  other keys cancel"
	case s.insert:
		return "enter send  tab priority  esc leave"
	case s.overlay == chatMatrix:
		return "j/k rows  h/l recipients  esc close"
	case s.overlay == chatPager:
		if s.narrow {
			return "j/k pgup/pgdn esc"
		}
		return "j/k scroll  pgup/pgdn page  esc close"
	}
	if s.narrow {
		return "i [ ] j/k R a e m s f o n ( ) :react"
	}
	return "i write  [ ] thread  j/k select  o other  n more  ( ) who  R reply  a answer  e expand  m matrix  s save  f file  :react"
}

// Update also registers :msg: the first message is the first moment a screen
// sees the model, which newChatScreen does not.
func (s *chatScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	s.register(m)
	s.track(m)
	if m.Mode != ModeCommand {
		s.msgErr = ""
	}
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		cmd = s.onKey(msg, m)
	case tea.PasteMsg:
		if s.prompt == nil {
			s.draft(m.CurrentThread).text += CleanBody(msg.Content)
		}
	case chatTickMsg:
		s.ticking = false
	case chatSentMsg:
		cmd = s.onSent(msg, m)
	case chatUploadedMsg:
		s.onUploaded(msg, m)
	case chatStartedMsg:
		cmd = s.onStarted(msg, m)
	case chatRefusedMsg:
		s.holdMsg(m, msg.cmd, msg.args, msg.why, msg.why)
	case chatReactedMsg:
		cmd = s.onReacted(msg, m)
	case chatAnsweredMsg:
		cmd = s.onAnswered(msg, m)
	case chatBlobMsg:
		s.onBlob(msg, m)
	case chatSavedMsg:
		s.onSaved(msg, m)
	case chatReadMsg:
		cmd = s.onRead(msg, m)
	case Loaded:
		// A reload of the thread (refresh, reconnect, an event) retries a
		// failed mark once.
		if id, ok := strings.CutPrefix(msg.Key, threadPrefix); ok && msg.Err == nil {
			delete(s.readFailed, id)
		}
	}
	if s.openNew != "" && chatLineFree(m) {
		cmd = tea.Batch(cmd, m.OpenThread(s.openNew))
		s.openNew = ""
	}
	s.reopenMsg(m)
	return tea.Batch(cmd, s.arm(), s.sync(m))
}

func (s *chatScreen) register(m *Model) {
	if s.registered {
		return
	}
	s.registered = true
	m.Commands["msg"] = Command{Help: "<peer-prefix> <text>  start a new thread with that peer", Run: s.commandMsg}
	m.Commands["attach"] = Command{Help: "[path]  add a file to the draft; ~/ expands, a run of spaces reads as one; alone, lists them", Run: s.commandAttach}
	m.Commands["detach"] = Command{Help: "[n]  remove the nth file from the draft, or every file", Run: s.commandDetach}
	m.Commands["react"] = Command{Help: "[n] <emoji|name> [--wake|--wake-idle|--hold]  toggle a reaction; n counts back from the newest message (1 = newest), default the selected one", Run: s.commandReact}
}

func (s *chatScreen) arm() tea.Cmd {
	if s.ticking {
		return nil
	}
	s.ticking = true
	return tea.Tick(chatPoll, func(time.Time) tea.Msg { return chatTickMsg{} })
}

// track resets everything that belongs to one thread when another is opened,
// whoever opened it.
func (s *chatScreen) track(m *Model) {
	if s.thread == m.CurrentThread {
		return
	}
	s.followList = true
	s.thread, s.sel, s.follow, s.top, s.fileIdx, s.overlay, s.whoFrom = m.CurrentThread, "", true, 0, 0, chatNoOverlay, 0
	delete(s.readFailed, m.CurrentThread) // reopening retries a failed mark
	if s.prompt != nil {
		s.prompt = nil
		m.Mode = ModeNormal
	}
}

// sync opens the first thread and marks the shown thread read, once this
// screen is the one on display. A Preferences overlay keeps the chat tab
// selected underneath, so the tab number alone is not visibility.
func (s *chatScreen) sync(m *Model) tea.Cmd {
	if m.active() != s || m.Help || m.stopped() {
		return nil
	}
	if m.CurrentThread == "" {
		if ids := s.selectable(m); len(ids) > 0 {
			s.followList = true
			return m.OpenThread(ids[0])
		}
	}
	st := m.Data.Thread[m.CurrentThread]
	if st == nil || st.Loading || len(st.Messages) == 0 {
		return nil
	}
	id, last := m.CurrentThread, st.Messages[len(st.Messages)-1].MessageID
	if s.readTo[id] == last || s.readAsk[id] != "" || s.readFailed[id] == last {
		return nil
	}
	s.readAsk[id] = last
	api, ctx := m.API, m.ctx
	return func() tea.Msg {
		r, err := api.ReadThread(ctx, id)
		return chatReadMsg{thread: id, last: last, marked: r.Marked, err: err}
	}
}

func (s *chatScreen) onRead(msg chatReadMsg, m *Model) tea.Cmd {
	delete(s.readAsk, msg.thread)
	if msg.err != nil {
		s.readFailed[msg.thread] = msg.last
		m.Toast("mark read: "+msg.err.Error(), true)
		return nil
	}
	delete(s.readFailed, msg.thread)
	s.readTo[msg.thread] = msg.last
	if msg.marked > 0 {
		return m.Reload("inbox", threadPrefix+msg.thread)
	}
	return nil
}

func (s *chatScreen) draft(id string) *chatDraft {
	d := s.drafts[id]
	if d == nil {
		d = &chatDraft{}
		s.drafts[id] = d
	}
	return d
}

// following is whether the selection rides the newest message. An open prompt
// holds it on the card the digit would answer.
func (s *chatScreen) following() bool { return s.follow && s.prompt == nil }

// index is the selected message's place in st, or -1 in an empty thread.
func (s *chatScreen) index(st *ThreadState) int {
	if st == nil {
		return -1
	}
	last := len(st.Messages) - 1
	if s.following() {
		return last
	}
	if i := slices.IndexFunc(st.Messages, func(msg envelope.Message) bool { return msg.MessageID == s.sel }); i >= 0 {
		return i
	}
	return last
}

func (s *chatScreen) selected(st *ThreadState) *envelope.Message {
	if i := s.index(st); i >= 0 {
		return &st.Messages[i]
	}
	return nil
}

func (s *chatScreen) move(st *ThreadState, by int) {
	i := s.index(st)
	if i < 0 {
		return
	}
	last := len(st.Messages) - 1
	i = min(max(i+by, 0), last)
	s.sel, s.follow, s.fileIdx = st.Messages[i].MessageID, i == last, 0
}

// step opens the thread by places from the current one in the list.
func (s *chatScreen) step(m *Model, by int) tea.Cmd {
	ids := s.selectable(m)
	i := slices.Index(ids, m.CurrentThread)
	next := i + by
	if i < 0 {
		if by < 0 {
			next = len(ids) - 1
		} else {
			next = 0
		}
	}
	if next < 0 || next >= len(ids) {
		return nil
	}
	cmd := m.OpenThread(ids[next])
	s.track(m)
	s.followList = true
	return cmd
}

func (s *chatScreen) onKey(msg tea.KeyPressMsg, m *Model) tea.Cmd {
	key := msg.String()
	if s.prompt != nil {
		return s.onPromptKey(key, m)
	}
	if m.Mode == ModeInsert {
		return s.onInsertKey(msg, m)
	}
	st := m.Data.Thread[m.CurrentThread]
	switch s.overlay {
	case chatMatrix:
		s.onMatrixKey(key)
		return nil
	case chatPager:
		s.onReaderKey(key)
		return nil
	}
	switch key {
	case "[":
		return s.step(m, -1)
	case "]":
		return s.step(m, 1)
	case "o":
		s.toggleOthers()
	case "n":
		return s.moreOthers(m)
	case "(":
		s.Wheel("chat.who", false, m)
	case ")":
		s.Wheel("chat.who", true, m)
	case "ctrl+u":
		s.Wheel("chat.list", false, m)
	case "ctrl+d":
		s.Wheel("chat.list", true, m)
	case "j", "down":
		s.move(st, 1)
	case "k", "up":
		s.move(st, -1)
	case "tab":
		chatCyclePrio(m)
	case "i":
		if m.CurrentThread == "" {
			m.Toast("no thread open: :msg <peer> <text> starts one", true)
			return nil
		}
		m.Mode = ModeInsert
	case "R":
		if picked := s.selected(st); picked != nil {
			s.draft(m.CurrentThread).reply = picked.MessageID
			m.Mode = ModeInsert
		}
	case "a":
		if refuseStopped(m) {
			return nil
		}
		s.startAnswer(m, st)
	case "e":
		return s.expand(m, st)
	case "m":
		if st != nil {
			s.overlay, s.matrixX, s.matrixY = chatMatrix, 0, 1<<30
		}
	case "f":
		if picked := s.selected(st); picked != nil && len(picked.Attachments) > 0 {
			s.fileIdx = (s.fileIdx + 1) % len(picked.Attachments)
		}
	case "s":
		return s.save(m, st)
	}
	return nil
}

func chatCyclePrio(m *Model) {
	m.Prio = chatPrios[(slices.Index(chatPrios, m.Prio)+1)%len(chatPrios)]
}

func (s *chatScreen) onInsertKey(msg tea.KeyPressMsg, m *Model) tea.Cmd {
	d := s.draft(m.CurrentThread)
	switch msg.String() {
	case "esc":
		m.Mode = ModeNormal
		if d.text == "" {
			d.reply = ""
		}
	case "enter":
		return s.send(m, d)
	case "tab":
		chatCyclePrio(m)
	case "backspace":
		if _, n := utf8.DecodeLastRuneInString(d.text); n > 0 {
			d.text = d.text[:len(d.text)-n]
		}
	default:
		d.text += CleanBody(msg.Text)
	}
	return nil
}

func (s *chatScreen) onMatrixKey(key string) {
	switch key {
	case "esc", "m":
		s.overlay = chatNoOverlay
	case "j", "down":
		s.matrixY++
	case "k", "up":
		s.matrixY--
	case "l", "right":
		s.matrixX++
	case "h", "left":
		s.matrixX--
	}
}

func (s *chatScreen) onReaderKey(key string) {
	switch key {
	case "esc", "e":
		s.overlay = chatNoOverlay
	case "j", "down":
		s.reader.top++
	case "k", "up":
		s.reader.top--
	case "pgdown", "space":
		s.reader.top += s.reader.rows
	case "pgup":
		s.reader.top -= s.reader.rows
	}
}

// chatKey is a fresh idempotency key.
func chatKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail
	return "tui-msg-" + hex.EncodeToString(b)
}

func (s *chatScreen) send(m *Model, d *chatDraft) tea.Cmd {
	id := m.CurrentThread
	if d.sending || strings.TrimSpace(d.text) == "" && len(d.atts) == 0 {
		return nil
	}
	if refuseStopped(m) { // the draft stays for after a restart
		return nil
	}
	for i, att := range d.atts {
		switch att.state {
		case chatAttUploading:
			m.Toast("wait: "+att.name+" is still uploading", true)
			return nil
		case chatAttFailed:
			m.Toast(fmt.Sprintf("%s did not upload: :detach %d removes it", att.name, i+1), true)
			return nil
		}
	}
	if len(d.atts) >= chatAttMax && len(d.text) > chatSpillAt {
		m.Toast(chatSpillSlot, true)
		return nil
	}
	attempt := chatAttempt{text: d.text, prio: m.Prio, reply: d.reply, shas: chatShas(d.atts)}
	if d.key == "" || d.attempt != attempt {
		d.key = chatKey()
	}
	d.attempt, d.sending = attempt, true
	req := envelope.SendRequest{IdempotencyKey: d.key, ThreadID: &id, Priority: attempt.prio, Content: envelope.SendContent{Text: attempt.text}}
	for _, att := range d.atts {
		req.Attachments = append(req.Attachments, envelope.Attachment{SHA256: att.sha, Size: att.size, MediaType: att.media, Name: att.name})
	}
	if attempt.reply != "" {
		req.InReplyTo = &attempt.reply
	}
	api, ctx := m.API, m.ctx
	return func() tea.Msg {
		r, err := api.Send(ctx, req)
		return chatSentMsg{thread: id, text: attempt.text, shas: attempt.shas, result: r, err: err}
	}
}

// chatShas is the attempt's view of the chips: every wire field of each done
// chip, in order, so the same bytes under another name or media type are a
// new request and mint a new key (PROTOCOL §5.3). A chip still uploading
// stands in by its id, so adding one during a send is a change, though it
// has no hash. Quoting keeps one chip's fields from running into the next.
func chatShas(atts []chatAtt) string {
	parts := make([]string, len(atts))
	for i, att := range atts {
		parts[i] = fmt.Sprintf("%s %d %q %q", att.sha, att.size, att.media, att.name)
		if att.state != chatAttDone {
			parts[i] = "chip:" + strconv.Itoa(att.id)
		}
	}
	return strings.Join(parts, ",")
}

func (s *chatScreen) onSent(msg chatSentMsg, m *Model) tea.Cmd {
	d := s.draft(msg.thread)
	d.sending = false
	if msg.err != nil {
		s.sendFailed(m, msg.thread, msg.err)
		return nil
	}
	d.key = ""
	if d.text == msg.text { // what was typed meanwhile stays a draft
		d.text, d.reply = "", ""
	}
	if chatShas(d.atts) == msg.shas { // so do files added or removed meanwhile
		d.atts = nil
	}
	if msg.thread == m.CurrentThread {
		s.follow = true
	}
	if msg.result.Spilled {
		m.Toast("sent, the hub spilled the full text to "+chatSpillName, false)
	} else {
		m.Toast("sent", false)
	}
	return m.Reload(threadPrefix+msg.thread, "threads") // a send of its own raises no event here
}

// sendFailed reports a refused composer send. The text stays in the composer,
// whatever the reason.
func (s *chatScreen) sendFailed(m *Model, id string, err error) {
	if isStatus(err, 401) {
		m.reject(err)
	}
	if id != m.CurrentThread { // enter here would send this thread's draft, not that one
		title := "untitled " + chatShort(id)
		if row := chatRowOf(m, id); row != nil {
			title = chatTitleOf(row.Thread)
		}
		m.Toast("send to "+title+" failed, its text is kept there: "+chatSendWhy(m, id, err), true)
		return
	}
	m.Toast(chatSendError(m, id, err), true)
}

// chatSendError is the toast for a failed send, on a surface where enter sends
// the same text again.
func chatSendError(m *Model, id string, err error) string {
	var e *opclient.Error
	if !errors.As(err, &e) {
		return "send failed, enter retries: " + chatSendWhy(m, id, err)
	}
	return chatSendWhy(m, id, err)
}

// chatSendWhy is what went wrong, with no promise about what a key does.
func chatSendWhy(m *Model, id string, err error) string {
	return diagText(chatSendWhyText(m, id, err))
}

func chatSendWhyText(m *Model, id string, err error) string {
	var e *opclient.Error
	switch {
	case !errors.As(err, &e):
		return err.Error()
	case e.Code == "thread_budget_exhausted":
		budget := 0
		if row := chatRowOf(m, id); row != nil && row.Thread.BudgetRemaining != nil {
			budget = *row.Thread.BudgetRemaining
		}
		return fmt.Sprintf("thread budget exhausted (budget %d), your text is kept", budget)
	case e.Code == "rate_limited":
		if after, ok := e.Details["retry_after_seconds"].(float64); ok {
			return fmt.Sprintf("%s: %s (retry after %.0fs)", e.Code, e.Message, after)
		}
	case e.Code == "payload_too_large" && e.Details["field"] == "attachments":
		return chatSpillSlot
	case e.Code == "":
		return e.Error()
	}
	return e.Code + ": " + e.Message
}

// chatUploadWhy is what a chip says about a failed upload.
func chatUploadWhy(m *Model, err error) string {
	return diagText(chatUploadWhyText(m, err))
}

func chatUploadWhyText(m *Model, err error) string {
	var e *opclient.Error
	switch {
	case !errors.As(err, &e):
		return err.Error()
	case e.Code == "blob_too_large":
		limit := int64(chatBlobMax) // a hub that leaves out the limit has §2.7's
		if v, ok := e.Details["limit"].(float64); ok && v > 0 {
			limit = int64(v)
		}
		return "too large, the hub's limit is " + chatSize(limit)
	case e.Code == "upload_quota_exceeded":
		if after, ok := e.Details["retry_after_seconds"].(float64); ok {
			at := m.Now().Add(time.Duration(after) * time.Second).In(m.loc).Format("15:04")
			return fmt.Sprintf("%s: %s (retry after %.0fs, at %s)", e.Code, e.Message, after, at)
		}
	case e.Code == "":
		return e.Error()
	}
	return e.Code + ": " + e.Message
}

// commandAttach is :attach PATH, which adds a chip to the open thread's draft
// and uploads at once, or :attach alone, which lists the chips. The path is the
// arguments joined by single spaces, since the command line splits on runs of
// whitespace, with a leading ~/ expanded. Every refusal comes before any upload
// and leaves the draft as it was.
func (s *chatScreen) commandAttach(m *Model, args []string) tea.Cmd {
	id := m.CurrentThread
	if id == "" {
		m.Toast("no thread open: open one, then :attach", true)
		return nil
	}
	d := s.draft(id)
	if len(args) == 0 {
		m.Toast(chatAttList(d.atts), false)
		return nil
	}
	if refuseStopped(m) {
		return nil
	}
	if len(d.atts) >= chatAttMax {
		m.Toast(fmt.Sprintf("attach: at most %d attachments per message", chatAttMax), true)
		return nil
	}
	path := strings.Join(args, " ")
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			m.Toast("attach: cannot expand ~/: "+err.Error(), true)
			return nil
		}
		path = filepath.Join(home, rest)
	}
	// Stat before open: opening a fifo would block the program's goroutine.
	st, err := os.Stat(path)
	if why := chatAttRefusal(path, st, err); why != "" {
		m.Toast(why, true)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		m.Toast("attach: "+err.Error(), true)
		return nil
	}
	// The open file is what uploads, so it is what the checks see.
	st, err = f.Stat()
	if why := chatAttRefusal(path, st, err); why != "" {
		_ = f.Close()
		m.Toast(why, true)
		return nil
	}
	s.nextAtt++
	ctx, cancel := context.WithCancel(m.ctx)
	att := chatAtt{id: s.nextAtt, name: chatAttName(path), size: st.Size(), media: chatMediaType(path), cancel: cancel, read: new(atomic.Int64)}
	d.atts = append(d.atts, att)
	api := m.API
	return func() tea.Msg {
		defer cancel()
		defer f.Close()
		up, err := api.Upload(ctx, &chatCounter{r: f, n: att.read}, att.size, att.media, att.name)
		return chatUploadedMsg{thread: id, id: att.id, att: up, err: err}
	}
}

// chatAttRefusal is why a path cannot be attached, or "" when it can.
func chatAttRefusal(path string, st fs.FileInfo, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "attach: no such file: " + path
	case err != nil:
		return "attach: " + err.Error()
	case st.IsDir():
		return "attach: " + path + " is a directory"
	case !st.Mode().IsRegular():
		return "attach: " + path + " is not a regular file"
	case st.Size() > chatBlobMax:
		return fmt.Sprintf("attach: %s is %s, over the 25 MiB limit", chatAttName(path), chatSize(st.Size()))
	}
	return ""
}

// chatAttName is the file's own name, cleaned and cut to chatAttNameMax
// characters.
func chatAttName(path string) string {
	name := Clean(filepath.Base(path))
	if r := []rune(name); len(r) > chatAttNameMax {
		name = string(r[:chatAttNameMax])
	}
	if name == "" {
		return "file"
	}
	return name
}

// chatMediaType is cli.mediaTypeOf's rule, which tui cannot import: the
// extension's type without parameters, else application/octet-stream. The
// content is never sniffed.
func chatMediaType(path string) string {
	mt := mime.TypeByExtension(filepath.Ext(path))
	if mt == "" {
		return "application/octet-stream"
	}
	if base, _, err := mime.ParseMediaType(mt); err == nil {
		return base
	}
	return mt
}

// chatAttList is the toast :attach alone shows.
func chatAttList(atts []chatAtt) string {
	if len(atts) == 0 {
		return "no attachments in this draft"
	}
	items := make([]string, len(atts))
	for i := range atts {
		items[i] = fmt.Sprintf("%d %s", i+1, atts[i].chip(chatAttNameMax))
	}
	return "attachments: " + strings.Join(items, ", ")
}

// chip is the chip's text, ASCII apart from the name, with the name cut to
// nameMax cells.
func (a *chatAtt) chip(nameMax int) string {
	name := ansi.Truncate(a.name, nameMax, "...")
	switch a.state {
	case chatAttDone:
		return "[+] " + name + " " + chatSize(a.size)
	case chatAttFailed:
		return "[x] " + name + ": " + a.err
	}
	pct := int64(0)
	if a.size > 0 {
		pct = min(a.read.Load()*100/a.size, 99) // 100 only once the hub has answered
	}
	return fmt.Sprintf("[..] %s %d%%", name, pct)
}

func (a *chatAtt) style() lipgloss.Style {
	switch a.state {
	case chatAttDone:
		return styleGood
	case chatAttFailed:
		return styleBad
	}
	return styleWarn
}

// onUploaded lands an upload on its chip, if the chip is still there.
func (s *chatScreen) onUploaded(msg chatUploadedMsg, m *Model) {
	d := s.drafts[msg.thread]
	if d == nil {
		return
	}
	i := slices.IndexFunc(d.atts, func(a chatAtt) bool { return a.id == msg.id })
	if i < 0 {
		return // detached while it was on its way
	}
	att := &d.atts[i]
	if msg.err != nil {
		if isStatus(msg.err, 401) {
			m.reject(msg.err)
		}
		// The hub's (or a proxy's) error text is untrusted: cleaned before
		// it reaches lipgloss.
		att.state, att.err = chatAttFailed, Clean(chatUploadWhy(m, msg.err))
		m.Toast(fmt.Sprintf("upload of %s failed: %s; :detach %d removes it", att.name, att.err, i+1), true)
		return
	}
	att.state, att.sha, att.size, att.media = chatAttDone, msg.att.SHA256, msg.att.Size, msg.att.MediaType
}

// commandDetach is :detach N, which removes the Nth chip (from 1) and stops its
// upload, or :detach alone, which removes them all.
func (s *chatScreen) commandDetach(m *Model, args []string) tea.Cmd {
	id := m.CurrentThread
	if id == "" {
		m.Toast("no thread open", true)
		return nil
	}
	d := s.draft(id)
	if len(args) > 1 {
		return usage(m, "detach")
	}
	if len(d.atts) == 0 {
		m.Toast("the draft has no attachments", true)
		return nil
	}
	if len(args) == 0 {
		for _, att := range d.atts {
			att.cancel()
		}
		n := len(d.atts)
		d.atts = nil
		m.Toast(fmt.Sprintf("detached %d attachments", n), false)
		return nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > len(d.atts) {
		m.Toast(fmt.Sprintf("no attachment %s: the draft has %d", args[0], len(d.atts)), true)
		return nil
	}
	att := d.atts[n-1]
	att.cancel()
	d.atts = slices.Delete(d.atts, n-1, n)
	m.Toast("detached "+att.name, false)
	return nil
}

// commandMsg is :msg <peer-prefix> <text>. The prefix matches a peer's name,
// case-insensitively, or its id; the send names the peer by id (§2.2 step 2).
func (s *chatScreen) commandMsg(m *Model, args []string) tea.Cmd {
	if len(args) < 2 {
		return s.refuseMsg(m, args, "usage: msg <peer-prefix> <text>")
	}
	if m.stopped() {
		return s.refuseMsg(m, args, m.Conn+": restart with a valid profile")
	}
	prefix := strings.ToLower(args[0])
	var ids, names []string
	for _, row := range m.Data.Peers {
		name := Clean(row.Peer.Name)
		if strings.HasPrefix(strings.ToLower(name), prefix) || strings.HasPrefix(row.Peer.PeerID, args[0]) {
			ids, names = append(ids, row.Peer.PeerID), append(names, name)
		}
	}
	switch len(ids) {
	case 0:
		return s.refuseMsg(m, args, "no peer matches "+args[0])
	case 1:
	default:
		return s.refuseMsg(m, args, fmt.Sprintf("ambiguous prefix %s: %s", args[0], strings.Join(names, ", ")))
	}
	attempt := chatNewAttempt{peer: ids[0], text: strings.Join(args[1:], " "), prio: m.Prio}
	// Each attempt keeps its key until it lands, so an unchanged retry of any
	// held line is the same request to the hub, however many are held.
	key, ok := s.newKeys[attempt]
	if !ok {
		key = chatKey()
		s.newKeys[attempt] = key
	}
	req := envelope.SendRequest{IdempotencyKey: key, Recipients: []string{attempt.peer}, Priority: attempt.prio, Content: envelope.SendContent{Text: attempt.text}}
	api, ctx, peer := m.API, m.ctx, names[0]
	return func() tea.Msg {
		r, err := api.Send(ctx, req)
		return chatStartedMsg{args: args, peer: peer, attempt: attempt, result: r, err: err}
	}
}

// chatReactFlags are :react's priority overrides.
var chatReactFlags = map[string]string{"--wake": "wake", "--wake-idle": "wake_idle", "--hold": "hold"}

// chatCount is whether s is :react's n: a positive decimal, no leading zero.
func chatCount(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// commandReact is :react [n] <emoji|name> [--wake|--wake-idle|--hold]. It
// toggles the operator's reaction on the selected message, or on the nth peer
// message counting back from the newest (PROTOCOL §17.6). With one positional
// argument it is always the emoji, so :react -1 is a thumbs down. Every
// refusal comes before the request and keeps the line.
func (s *chatScreen) commandReact(m *Model, args []string) tea.Cmd {
	refuse := func(why string) tea.Cmd { return s.refuseLine(m, "react", args, why) }
	const syntax = "usage: react [n] <emoji|name> [--wake|--wake-idle|--hold]"
	var pos []string
	prio, flags := "", 0
	for _, arg := range args {
		if p, ok := chatReactFlags[arg]; ok {
			prio, flags = p, flags+1
			continue
		}
		pos = append(pos, arg)
	}
	if flags > 1 {
		return refuse("one priority flag at most: --wake, --wake-idle or --hold")
	}
	n := 0
	switch {
	case len(pos) == 1:
	case len(pos) == 2 && chatCount(pos[0]):
		var err error
		if n, err = strconv.Atoi(pos[0]); err != nil {
			return refuse(syntax)
		}
		pos = pos[1:]
	default:
		return refuse(syntax)
	}
	if m.stopped() {
		return refuse(m.Conn + ": restart with a valid profile")
	}
	id := m.CurrentThread
	st := m.Data.Thread[id]
	if id == "" || st == nil {
		return refuse("no open thread: [ and ] open one")
	}
	var target *envelope.Message
	if n > 0 {
		var peers []*envelope.Message
		for i := range st.Messages {
			if st.Messages[i].Sender != "hub" {
				peers = append(peers, &st.Messages[i])
			}
		}
		if n > len(peers) {
			return refuse(fmt.Sprintf("no message %d: the thread has %d", n, len(peers)))
		}
		target = peers[len(peers)-n]
	} else {
		target = s.selected(st)
		if target != nil && target.Sender == "hub" {
			return refuse("the selected line is the hub's: select a message, or give its number")
		}
	}
	if target == nil {
		return refuse("no message to react to")
	}
	emoji, ok := envelope.ParseReaction(pos[0])
	if !ok {
		return refuse("unknown reaction " + pos[0] + ": use one of " + reactionNames())
	}
	req := envelope.ReactionRequest{MessageID: target.MessageID, Emoji: emoji, Op: "add", Priority: prio}
	me := m.Data.Inbox.Me.PeerID
	for _, g := range target.Reactions {
		if g.Emoji == emoji && me != "" && slices.Contains(g.Peers, me) {
			req.Op, req.Priority = "remove", ""
		}
	}
	if req.Op == "add" && req.Priority == "" {
		req.Priority = envelope.DefaultReactionPriority(emoji)
	}
	api, ctx := m.API, m.ctx
	return func() tea.Msg {
		r, err := api.React(ctx, req)
		return chatReactedMsg{thread: id, message: req.MessageID, emoji: emoji, op: req.Op, result: r, err: err}
	}
}

// onReacted reloads the thread from the reacted message on. The response's
// aggregate is not applied: it carries no revision, so a response that
// arrives after the tail loaded a newer peer reaction would roll it back.
func (s *chatScreen) onReacted(msg chatReactedMsg, m *Model) tea.Cmd {
	if msg.err != nil {
		if isStatus(msg.err, 401) {
			m.reject(msg.err)
		}
		m.Toast("react failed: "+chatSendWhy(m, msg.thread, msg.err), true)
		return nil
	}
	m.rewindTo(msg.thread, msg.message)
	verb := "reacted "
	if msg.op == "remove" {
		verb = "removed "
	}
	// Names, as on the chips: terminals disagree on emoji widths.
	m.Toast(verb+envelope.ReactionName(msg.emoji), false)
	return m.Reload(threadPrefix + msg.thread)
}

// reactionNames lists the fixed set by name in display order, for TUI text
// that must keep exact cell widths.
func reactionNames() string {
	names := make([]string, len(envelope.ReactionEmoji))
	for i, emoji := range envelope.ReactionEmoji {
		names[i] = envelope.ReactionName(emoji)
	}
	return strings.Join(names, " ")
}

// refuseMsg is a :msg stopped here. The line comes back through a message, so
// the shell has finished closing it by then, whichever way round it does that.
func (s *chatScreen) refuseMsg(m *Model, args []string, why string) tea.Cmd {
	return s.refuseLine(m, "msg", args, why)
}

// refuseLine is refuseMsg for any chat command that keeps its line.
func (s *chatScreen) refuseLine(m *Model, cmd string, args []string, why string) tea.Cmd {
	m.Toast(why, true)
	return func() tea.Msg { return chatRefusedMsg{cmd: cmd, args: args, why: why} }
}

// chatLineFree is whether the command line can be taken without taking it from
// the operator, who may have moved on while the send was out.
func chatLineFree(m *Model) bool { return m.Mode == ModeNormal && !m.Help }

// holdMsg keeps a :msg that did not go until the line is free, which Update
// checks after every message. why goes with the line; reason says what happened
// until then, because enter does not retry it yet.
func (s *chatScreen) holdMsg(m *Model, cmd string, args []string, why, reason string) {
	if cmd == "" {
		cmd = "msg"
	}
	s.heldMsgs = append(s.heldMsgs, chatHeldMsg{cmd, args, why})
	if chatLineFree(m) {
		return
	}
	m.Toast(":"+cmd+" failed, returns to the command line when you finish: "+reason, true)
	if m.Mode == ModeCommand {
		s.showMsgErr(m)
	}
}

// reopenMsg puts the oldest held :msg back on the command line.
func (s *chatScreen) reopenMsg(m *Model) {
	if len(s.heldMsgs) == 0 || !chatLineFree(m) {
		return
	}
	held := s.heldMsgs[0]
	s.heldMsgs = s.heldMsgs[1:]
	m.Mode, m.Input = ModeCommand, strings.TrimSpace(held.cmd+" "+strings.Join(held.args, " "))
	m.Toast(held.why, true)
	s.showMsgErr(m)
}

// showMsgErr moves the toast to the composer row, because an open command line
// covers the toast. Only the chat tab shows that row; :msg lands there anyway.
func (s *chatScreen) showMsgErr(m *Model) {
	// The composer row that carries the failure is on Chat. Leave Preferences
	// so that navigation is actually visible.
	m.prefsOn = false
	m.Tab, s.msgErr, s.overlay = tabChat, m.toast.text, chatNoOverlay
}

func (s *chatScreen) onStarted(msg chatStartedMsg, m *Model) tea.Cmd {
	if msg.err != nil {
		if isStatus(msg.err, 401) {
			m.reject(msg.err)
		}
		s.holdMsg(m, "msg", msg.args, chatSendError(m, "", msg.err), chatSendWhy(m, "", msg.err))
		return nil
	}
	delete(s.newKeys, msg.attempt)
	if !chatLineFree(m) {
		// The operator is typing or reading: the new thread waits, so keys keep
		// going where they went. Update opens it once the line is free.
		if msg.result.ThreadID != "" {
			s.openNew = msg.result.ThreadID
		}
		m.Toast("sent to "+msg.peer+", its thread opens when you finish", false)
		return m.Reload("threads")
	}
	m.Toast("sent to "+msg.peer, false)
	return tea.Batch(m.OpenThread(msg.result.ThreadID), m.Reload("threads"))
}

// startAnswer opens the digit prompt. It is Insert mode, because the shell
// keeps 1-4 for tabs otherwise.
func (s *chatScreen) startAnswer(m *Model, st *ThreadState) {
	a := s.pickApproval(st)
	if a == nil {
		m.Toast("no open approval addressed to you here", true)
		return
	}
	switch s.answers[a.ApprovalID] {
	case chatAnswering:
		m.Toast("the answer is already on its way", true)
		return
	case chatAnswered:
		m.Toast("already answered", true)
		return
	case chatClosed:
		m.Toast("approval closed", true)
		return
	}
	if chatExpired(m, a.ExpiresAt) {
		m.Toast("approval expired", true)
		return
	}
	i := slices.IndexFunc(st.Messages, func(msg envelope.Message) bool { return msg.MessageID == a.MessageID })
	if i < 0 {
		m.Toast("that approval's message has not loaded yet", true)
		return
	}
	s.move(st, i-s.index(st)) // the card is on screen before the digit is taken
	card := &st.Messages[i]
	question := card.Content.Text
	if card.ApprovalRequest != nil {
		question = card.ApprovalRequest.Question
	}
	s.prompt = &opapi.OpenApproval{ApprovalID: a.ApprovalID, ThreadID: m.CurrentThread, MessageID: a.MessageID, Requester: card.Sender, Question: question, ExpiresAt: a.ExpiresAt, Options: a.Options}
	m.Mode = ModeInsert
}

// pickApproval is the selected message's approval, else the newest one here
// that waits on the operator, which startAnswer then selects. A card addressed
// to someone else is read-only.
func (s *chatScreen) pickApproval(st *ThreadState) *opapi.ThreadApproval {
	if st == nil {
		return nil
	}
	picked := s.selected(st)
	var newest *opapi.ThreadApproval
	for i := range st.Detail.OpenApprovals {
		a := &st.Detail.OpenApprovals[i]
		if !a.AddressedToMe {
			continue
		}
		if picked != nil && a.MessageID == picked.MessageID {
			return a
		}
		newest = a
	}
	return newest
}

func (s *chatScreen) onPromptKey(key string, m *Model) tea.Cmd {
	a := *s.prompt
	s.prompt = nil
	m.Mode = ModeNormal
	if len(key) != 1 || key[0] < '1' || int(key[0]-'1') >= len(a.Options) {
		return nil
	}
	if chatExpired(m, a.ExpiresAt) { // it can pass while the prompt is open
		m.Toast("approval expired", true)
		return nil
	}
	if refuseStopped(m) { // the credential can be refused while the prompt is open
		return nil
	}
	option := int(key[0] - '1')
	s.answers[a.ApprovalID] = chatAnswering
	api, ctx := m.API, m.ctx
	return func() tea.Msg { return chatAnsweredMsg{approval: a, option: option, err: api.Answer(ctx, a, option)} }
}

func (s *chatScreen) onAnswered(msg chatAnsweredMsg, m *Model) tea.Cmd {
	id := msg.approval.ApprovalID
	reload := m.Reload("inbox", threadPrefix+msg.approval.ThreadID)
	var e *opclient.Error
	switch {
	case msg.err == nil:
		s.answers[id] = chatAnswered
		m.Toast("answered: "+msg.approval.Options[msg.option], false)
	case errors.As(msg.err, &e) && e.Code == "approval_closed":
		s.answers[id] = chatClosed
		m.Toast("approval closed: it was answered or expired elsewhere", true)
	default:
		delete(s.answers, id) // only this client failed: the operator may try again
		if isStatus(msg.err, 401) {
			m.reject(msg.err)
		}
		m.Toast("answer not sent: "+msg.err.Error(), true)
	}
	return reload
}

// chatExpired reads the deadline against the hub's clock (Review Focus 3).
func chatExpired(m *Model, at string) bool {
	t, err := envelope.ParseTime(at)
	return err == nil && !m.HubNow().Before(t)
}

// chatSpill is a spilled message's full-text attachment: the hub appends it
// last, and a peer's own attachment may share the name.
func chatSpill(msg *envelope.Message) (envelope.Attachment, bool) {
	if spilled, _ := msg.Metadata[chatSpillMeta].(bool); !spilled {
		return envelope.Attachment{}, false
	}
	for i := len(msg.Attachments) - 1; i >= 0; i-- {
		if msg.Attachments[i].Name == chatSpillName {
			return msg.Attachments[i], true
		}
	}
	return envelope.Attachment{}, false
}

func chatIsText(media string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(media)), "text/")
}

// expand opens the reader. A spilled message fetches its full text, which must
// be text/* and at most chatExpandMax; any other message is already whole, and
// this is the way to read one too tall for the log.
func (s *chatScreen) expand(m *Model, st *ThreadState) tea.Cmd {
	picked := s.selected(st)
	if picked == nil {
		return nil
	}
	sender := chatNames(m).of(picked.Sender)
	att, spilled := chatSpill(picked)
	if !spilled {
		s.reader = chatReader{message: picked.MessageID, title: "message · " + sender, body: CleanBody(picked.Content.Text)}
		s.overlay = chatPager
		return nil
	}
	if !chatIsText(att.MediaType) {
		m.Toast("refused: "+att.MediaType+" is not text", true)
		return nil
	}
	if att.Size > chatExpandMax {
		m.Toast(fmt.Sprintf("refused: %s is over 1 MiB", chatSize(att.Size)), true)
		return nil
	}
	s.reader = chatReader{message: picked.MessageID, title: chatSpillName + " · " + sender, loading: true}
	s.overlay = chatPager
	api, ctx, id, sha := m.API, m.ctx, picked.MessageID, att.SHA256
	return func() tea.Msg {
		data, media, err := api.Blob(ctx, sha, chatExpandMax)
		return chatBlobMsg{message: id, data: data, media: media, err: err}
	}
}

func (s *chatScreen) onBlob(msg chatBlobMsg, m *Model) {
	if s.overlay != chatPager || s.reader.message != msg.message {
		return // closed, or another message opened, while it was on its way
	}
	switch {
	case msg.err != nil && strings.Contains(msg.err.Error(), "blob exceeds"):
		m.Toast("refused: the text is over 1 MiB", true)
	case msg.err != nil:
		m.Toast("could not fetch the text: "+msg.err.Error(), true)
	case !chatIsText(msg.media):
		m.Toast("refused: "+msg.media+" is not text", true)
	default:
		s.reader.body, s.reader.loading = CleanBody(string(msg.data)), false
		return
	}
	s.overlay = chatNoOverlay
}

// save writes the selected file into the working directory.
func (s *chatScreen) save(m *Model, st *ThreadState) tea.Cmd {
	picked := s.selected(st)
	if picked == nil || len(picked.Attachments) == 0 {
		m.Toast("no file on the selected message", true)
		return nil
	}
	att := picked.Attachments[min(s.fileIdx, len(picked.Attachments)-1)]
	if slices.ContainsFunc(st.Detail.Files, func(f opapi.ThreadFile) bool { return f.SHA256 == att.SHA256 && f.Purged }) {
		m.Toast("refused: that file was purged", true)
		return nil
	}
	api, ctx := m.API, m.ctx
	return func() tea.Msg {
		data, _, err := api.Blob(ctx, att.SHA256, chatBlobMax)
		if err != nil {
			return chatSavedMsg{err: err}
		}
		name, err := chatWrite(chatSaveName(att.Name, att.SHA256), data)
		return chatSavedMsg{name: name, err: err}
	}
}

func (s *chatScreen) onSaved(msg chatSavedMsg, m *Model) {
	if msg.err != nil {
		m.Toast("save failed: "+msg.err.Error(), true)
		return
	}
	m.Toast("saved as "+msg.name, false)
}

// chatSaveName reduces a hostile name to one component of the working
// directory. A name with nothing left in it falls back to the blob's hash.
func chatSaveName(name, sha string) string {
	base := filepath.Base(Clean(name))
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return "blob-" + filepath.Base(Clean(sha[:min(len(sha), 16)]))
	}
	return base
}

// chatWrite creates base, or `base (n).ext` when that exists. O_EXCL makes
// never-overwrite hold against a race and a planted link alike.
func chatWrite(base string, data []byte) (string, error) {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	for n := range chatSaveTries {
		name := base
		if n > 0 {
			name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if err = errors.Join(err, f.Close()); err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("every name for %s is taken", base)
}

// View draws the three panes above the composer, or an overlay.
func (s *chatScreen) View(m *Model, w, h int) string {
	s.track(m)
	s.insert, s.narrow = m.Mode == ModeInsert, w < chatHintsWide
	st := m.Data.Thread[m.CurrentThread]
	switch {
	case s.overlay == chatMatrix && st != nil:
		m.addHit(m.hitX, m.hitY, w, h, hitScroll, "chat.matrix")
		return s.viewMatrix(m, st, w, h)
	case s.overlay == chatPager:
		m.addHit(m.hitX, m.hitY, w, h, hitScroll, "chat.pager")
		return s.viewReader(w, h)
	}
	paneH := h - 1
	chips := s.viewChips(m, w)
	if chips != "" {
		paneH-- // the panes give the chip row theirs
	}
	listW := min(max(w*25/140, 14), 30)
	whoW := min(max(w*30/140, 22), 34)
	logW := w - listW - whoW
	list := Pane("threads [ ]", listW, paneH, false, s.renderList(m, listW, paneH))
	log := Pane(chatLogTitle(m, logW), logW, paneH, true, s.viewLog(m, st, logW-2, paneH-2))
	who := Pane("who", whoW, paneH, false, s.viewWho(m, st, whoW-2, paneH-2))
	m.addHit(m.hitX+listW, m.hitY, logW, paneH, hitScroll, "chat.log")
	m.addHit(m.hitX+listW+logW, m.hitY, whoW, paneH, hitScroll, "chat.who")
	out := lipgloss.JoinHorizontal(lipgloss.Top, list, log, who) + "\n"
	if chips != "" {
		out += chips + "\n"
	}
	return out + s.viewComposer(m, st, w)
}

// viewChips is the row above the composer, while the draft has attachments.
// Chips that do not fit give way to a count.
func (s *chatScreen) viewChips(m *Model, w int) string {
	d := s.drafts[m.CurrentThread]
	if m.CurrentThread == "" || d == nil || len(d.atts) == 0 {
		return ""
	}
	const gap = "  "
	var b strings.Builder
	used := 0
	for i := range d.atts {
		text := d.atts[i].chip(chatChipNameMax)
		sep := ""
		if i > 0 {
			sep = gap
		}
		more := "" // the count the rest would need, should they not fit
		if i < len(d.atts)-1 {
			more = fmt.Sprintf("%s+%d more", gap, len(d.atts)-i-1)
		}
		if used+len(sep)+lipgloss.Width(text)+len(more) > w {
			if i > 0 {
				b.WriteString(styleMuted.Render(fmt.Sprintf("%s+%d more", gap, len(d.atts)-i)))
				break
			}
			text = ansi.Truncate(text, max(w-len(more), 1), "...")
		}
		b.WriteString(sep + d.atts[i].style().Render(text))
		used += len(sep) + lipgloss.Width(text)
	}
	return fit(b.String(), w)
}

// chatRowOf is the freshest row held for a thread.
func chatRowOf(m *Model, id string) *opapi.ThreadRow {
	if st := m.Data.Thread[id]; st != nil && st.Detail.Row.Thread.ThreadID == id {
		return &st.Detail.Row
	}
	rows := m.Data.Threads.Threads
	if i := slices.IndexFunc(rows, func(r opapi.ThreadRow) bool { return r.Thread.ThreadID == id }); i >= 0 {
		return &rows[i]
	}
	return nil
}

func chatTitleOf(t envelope.ThreadSummary) string {
	if t.Title != nil {
		if title := Clean(*t.Title); title != "" {
			return title
		}
	}
	return "untitled " + chatShort(t.ThreadID)
}

// chatShort is an id with its middle gone: thr_…3P5R.
func chatShort(id string) string {
	r := []rune(Clean(id))
	if len(r) <= 12 {
		return string(r)
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// chatLogTitle fits a pane w wide: the title gives way, so the budget stays.
func chatLogTitle(m *Model, w int) string {
	row := chatRowOf(m, m.CurrentThread)
	if row == nil {
		return "chat"
	}
	budget := "unlimited"
	if b := row.Thread.BudgetRemaining; b != nil {
		budget = fmt.Sprint(*b)
	}
	facts := fmt.Sprintf("  %s · budget %s", chatShort(row.Thread.ThreadID), budget)
	if row.Cut != nil {
		facts += " · cut " + Clean(row.Cut.Reason)
	}
	return ansi.Truncate(chatTitleOf(row.Thread), max(w-5-lipgloss.Width(facts), 8), "…") + facts
}

// chatSeg is a run of cleaned text in one style.
type chatSeg struct {
	text  string
	style lipgloss.Style
}

// chatRow is one line of the log. Selection shades the whole line, so it has to
// be applied per segment: a style's reset would end an outer background.
type chatRow []chatSeg

func (r chatRow) render(w int, selected bool) string {
	var b strings.Builder
	used := 0
	for _, seg := range r {
		style := seg.style
		if selected {
			style = style.Background(colorPick)
		}
		b.WriteString(style.Render(seg.text))
		used += lipgloss.Width(seg.text)
	}
	if selected && used < w && len(r) > 0 { // a gap between messages stays unshaded
		b.WriteString(lipgloss.NewStyle().Background(colorPick).Render(strings.Repeat(" ", w-used)))
	}
	return b.String()
}

// chatPeers maps peer ids to cleaned display names.
type chatPeers map[string]string

func chatNames(m *Model) chatPeers {
	names := make(chatPeers, len(m.Data.Peers))
	for _, row := range m.Data.Peers {
		names[row.Peer.PeerID] = Clean(row.Peer.Name)
	}
	return names
}

func (p chatPeers) of(id string) string {
	if name := p[id]; name != "" {
		return name
	}
	return chatShort(id)
}

// short is the part of a name a receipt or reply marker has room for.
func (p chatPeers) short(id string) string {
	name, _, _ := strings.Cut(p.of(id), "-")
	return ansi.Truncate(name, 8, "")
}

func chatSenderStyle(id string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return styleSender[h.Sum32()%uint32(len(styleSender))]
}

func chatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func chatClock(m *Model, at string) string {
	t, err := envelope.ParseTime(at)
	if err != nil {
		return "--:--"
	}
	return t.In(m.loc).Format("15:04")
}

// chatHang wraps a message body under its head: the first line starts lead
// cells in, and every later line sits in the chatIndent gutter with the file,
// job and receipt rows, so the transcript keeps its left edge.
func chatHang(text string, lead, width int) []string {
	gutter := len(chatIndent)
	if width <= 2*gutter || lead < gutter {
		return chatWrap(text, lead, width)
	}
	lines := chatWrap(text, lead-gutter, width-gutter)
	for i := 1; i < len(lines); i++ {
		lines[i] = chatIndent + lines[i]
	}
	return lines
}

// chatWrap wraps text to width, the first line starting lead cells in.
func chatWrap(text string, lead, width int) []string {
	hold := strings.Repeat(chatHold, lead)
	lines := strings.Split(ansi.Wrap(hold+text, width, ""), "\n")
	lines[0] = strings.TrimPrefix(lines[0], hold)
	return lines
}

func chatReceipt(display string) (string, lipgloss.Style) {
	switch display {
	case "queued":
		return "·", styleMuted
	case "pushed":
		return "›", styleMuted
	case "accepted":
		return "✓", styleAccepted
	case "processed":
		return "✓✓", styleGood
	case "read":
		return "●", styleText
	case "failed", "dead_letter", "expired":
		return "✗", styleBad
	}
	return "?", styleMuted
}

// chatLog is what drawing the log needs, gathered once a frame.
type chatLog struct {
	s       *chatScreen
	m       *Model
	names   chatPeers
	byID    map[string]*envelope.Message
	cells   map[string][]opapi.MatrixCell
	open    map[string]opapi.ThreadApproval
	decided map[string]string
	width   int
	body    int // wrapped lines one message may fill
	gap     bool
}

func (s *chatScreen) newChatLog(m *Model, st *ThreadState, w, h int) *chatLog {
	l := &chatLog{s: s, m: m, names: chatNames(m), width: w, body: max(h-8, chatPreview), gap: h >= 30,
		byID:    chatMessages(st),
		cells:   make(map[string][]opapi.MatrixCell, len(st.Matrix.Rows)),
		open:    make(map[string]opapi.ThreadApproval, len(st.Detail.OpenApprovals)),
		decided: map[string]string{},
	}
	for i := range st.Messages {
		if r := st.Messages[i].ApprovalResponse; r != nil {
			l.decided[r.ApprovalID] = Clean(r.Decision)
		}
	}
	for _, row := range st.Matrix.Rows {
		l.cells[row.MessageID] = row.Cells
	}
	for _, a := range st.Detail.OpenApprovals {
		l.open[a.MessageID] = a
	}
	return l
}

func chatMessages(st *ThreadState) map[string]*envelope.Message {
	byID := make(map[string]*envelope.Message, len(st.Messages))
	for i := range st.Messages {
		byID[st.Messages[i].MessageID] = &st.Messages[i]
	}
	return byID
}

// item is one message's lines. The file marker shows on the selected message.
func (l *chatLog) item(msg *envelope.Message, selected bool) []chatRow {
	var rows []chatRow
	if msg.Sender == "hub" {
		rows = l.system(msg)
	} else {
		rows = l.head(msg)
		rows = append(rows, l.files(msg, selected)...)
		rows = append(rows, l.job(msg)...)
		rows = append(rows, l.approval(msg)...)
		if row := l.reactions(msg, selected); row != nil {
			rows = append(rows, row)
		}
		if row := l.receipts(msg); row != nil {
			rows = append(rows, row)
		}
	}
	if l.gap {
		rows = append(rows, chatRow{})
	}
	return rows
}

// system is a hub-authored message: one quiet line.
func (l *chatLog) system(msg *envelope.Message) []chatRow {
	text := CleanBody(msg.Content.Text)
	if r := msg.ApprovalResponse; r != nil && r.Decision == "expired" {
		text = "approval expired"
	}
	line := strings.ReplaceAll(fmt.Sprintf("-- %s %s", chatClock(l.m, msg.CreatedAt), text), "\n", " ")
	return []chatRow{{{ansi.Truncate(line, l.width, "…"), styleMuted}}}
}

// head is the time, priority glyph, sender and reply marker, then the text.
func (l *chatLog) head(msg *envelope.Message) []chatRow {
	glyph := chatSeg{" ", styleMuted}
	switch msg.Priority {
	case "wake":
		glyph = chatSeg{"!", styleWarn}
	case "wake_idle":
		glyph = chatSeg{"~", styleCyan}
	}
	name := ansi.Truncate(l.names.of(msg.Sender), min(20, l.width/3), "…")
	segs := chatRow{{chatClock(l.m, msg.CreatedAt) + " ", styleMuted}, glyph, {"<" + name + ">", chatSenderStyle(msg.Sender)}}
	if label := chatClientLabel(msg); label != "" {
		segs = append(segs, chatSeg{" [" + ansi.Truncate(label, max(l.width/2, 12), "…") + "]", styleMuted})
	}
	if msg.InReplyTo != nil {
		segs = append(segs, chatSeg{" " + chatReplyMark(l.m, l.names, l.byID, *msg.InReplyTo), styleMuted})
	}
	segs = append(segs, chatSeg{" ", styleText})
	lead := 0
	for _, seg := range segs {
		lead += lipgloss.Width(seg.text)
	}
	lines := chatHang(l.text(msg), lead, l.width)
	_, spilled := chatSpill(msg)
	limit := l.body
	if spilled {
		limit = chatPreview
	}
	hidden := len(lines) - limit
	if hidden > 0 {
		lines = lines[:limit]
		lines[limit-1] = ansi.Truncate(lines[limit-1], l.width-1, "") + "…"
	}
	rows := []chatRow{append(segs, chatSeg{lines[0], styleText})}
	for _, line := range lines[1:] {
		rows = append(rows, chatRow{{line, styleText}})
	}
	if hidden > 0 && !spilled {
		rows = append(rows, chatRow{{fmt.Sprintf("[+%d lines, e reads it all]", hidden), styleMuted}})
	}
	return rows
}

// chatClientLabel is the hub-set operator CLI label of msg (PROTOCOL §2.2
// step 11), flattened to one line, or "". Peers cannot write vecnet.* keys,
// so only the hub's own value shows.
func chatClientLabel(msg *envelope.Message) string {
	c, _ := msg.Metadata[envelope.ClientMetadataKey].(map[string]any)
	if c["kind"] != envelope.ClientKindOperatorCLI {
		return ""
	}
	label, _ := c["label"].(string)
	return strings.Join(strings.Fields(Clean(label)), " ")
}

// chatReplyMark names a reply's parent, which may be outside the loaded page.
func chatReplyMark(m *Model, names chatPeers, byID map[string]*envelope.Message, id string) string {
	parent := byID[id]
	if parent == nil {
		return "↳" + chatShort(id)
	}
	return "↳" + names.short(parent.Sender) + "@" + chatClock(m, parent.CreatedAt)
}

func (l *chatLog) text(msg *envelope.Message) string {
	if r := msg.ApprovalRequest; r != nil && r.Question == msg.Content.Text {
		return "" // the card below says it
	}
	return strings.TrimRight(strings.ReplaceAll(CleanBody(msg.Content.Text), "\t", "    "), "\n")
}

func (l *chatLog) files(msg *envelope.Message, selected bool) []chatRow {
	var rows []chatRow
	spill, spilled := chatSpill(msg)
	for i, att := range msg.Attachments {
		mark := " "
		if selected && i == min(l.s.fileIdx, len(msg.Attachments)-1) {
			mark = ">"
		}
		name := Clean(att.Name)
		if name == "" {
			name = chatShort(att.SHA256)
		}
		if spilled && att.SHA256 == spill.SHA256 {
			extra := max(att.Size-int64(len(msg.Content.Text)), 0)
			rows = append(rows, chatRow{{chatIndent[1:] + mark, styleMuted}, {fmt.Sprintf("[+%s spilled → %s]", chatSize(extra), name), styleMuted}})
			continue
		}
		rows = append(rows, chatRow{{chatIndent[1:] + mark, styleMuted}, {"file " + name, styleCyan}, {" " + chatSize(att.Size), styleMuted}})
	}
	return rows
}

func (l *chatLog) job(msg *envelope.Message) []chatRow {
	var rows []chatRow
	if r := msg.JobRequest; r != nil {
		rows = append(rows, chatRow{{chatIndent, styleMuted}, {"job request " + Clean(r.Operation), styleGood}, {" key " + Clean(r.JobKey), styleMuted}})
	}
	if r := msg.JobStatus; r != nil {
		row := chatRow{{chatIndent, styleMuted}, {fmt.Sprintf("job %s %s seq %d", chatShort(r.JobID), Clean(r.State), r.Sequence), styleGood}}
		if len(r.Output) > 0 {
			row = append(row, chatSeg{" " + Clean(string(r.Output)), styleMuted})
		}
		rows = append(rows, row)
	}
	return rows
}

// approval is the card under an approval request. Only a card that is open,
// addressed to the operator and unexpired by the hub's clock offers digits.
func (l *chatLog) approval(msg *envelope.Message) []chatRow {
	req := msg.ApprovalRequest
	if req == nil {
		return nil
	}
	options := make([]string, len(req.Options))
	for i, option := range req.Options {
		options[i] = Clean(option)
	}
	text, tail := "? "+Clean(req.Question), ""
	a, open := l.open[msg.MessageID]
	state := l.s.answers[req.ApprovalID]
	switch {
	case state == chatAnswering:
		tail = "answering"
	case state == chatAnswered:
		tail = "answered"
	case state == chatClosed:
		tail = "closed"
	case !open && l.decided[req.ApprovalID] == "expired":
		tail = "expired"
	case !open && l.decided[req.ApprovalID] != "":
		tail = "answered: " + l.decided[req.ApprovalID]
	case !open:
		tail = "closed"
	case chatExpired(l.m, a.ExpiresAt):
		tail = "expired"
	case a.AddressedToMe:
		for i, option := range options {
			text += fmt.Sprintf(" [%d]%s", i+1, option)
		}
		tail = l.countdown(a.ExpiresAt)
	default:
		text += " " + strings.Join(options, " / ")
		tail = "read-only " + l.countdown(a.ExpiresAt)
	}
	var rows []chatRow
	for _, line := range chatWrap(text+" "+tail, 0, l.width-len(chatIndent)) {
		rows = append(rows, chatRow{{chatIndent + line, styleAccent}})
	}
	return rows
}

// countdown uses the hub's clock; the local time beside it is only a label.
func (l *chatLog) countdown(at string) string {
	t, err := envelope.ParseTime(at)
	if err != nil {
		return ""
	}
	left := t.Sub(l.m.HubNow())
	text := fmt.Sprintf("%dm", int(left.Minutes()))
	if left >= time.Hour {
		text = fmt.Sprintf("%dh%02dm", int(left.Hours()), int(left.Minutes())%60)
	}
	return "exp " + t.In(l.m.loc).Format("15:04") + " " + text
}

// receipts is one compact marker per recipient, cut with a count when it runs
// past the line.
func (l *chatLog) receipts(msg *envelope.Message) chatRow {
	cells := l.cells[msg.MessageID]
	if len(cells) == 0 {
		return nil
	}
	row := chatRow{{chatIndent, styleMuted}}
	used := len(chatIndent)
	for i, cell := range cells {
		glyph, style := chatReceipt(Clean(cell.Display))
		text := l.names.short(cell.Recipient) + glyph + " "
		if used+lipgloss.Width(text)+8 > l.width && i < len(cells)-1 {
			return append(row, chatSeg{fmt.Sprintf("+%d more", len(cells)-i), styleMuted})
		}
		used += lipgloss.Width(text)
		row = append(row, chatSeg{text, style})
	}
	return row
}

// reactions is one ASCII chip per emoji, because terminals disagree on emoji
// widths: [+1 2] [check 1*], where * marks the operator's own reaction. The
// selected message names its reactors instead: [check: codex, you]. Like
// receipts, it is cut with a count when it runs past the line.
func (l *chatLog) reactions(msg *envelope.Message, selected bool) chatRow {
	if len(msg.Reactions) == 0 {
		return nil
	}
	me := l.m.Data.Inbox.Me.PeerID
	row := chatRow{{chatIndent, styleMuted}}
	used := len(chatIndent)
	for i, g := range msg.Reactions {
		name := envelope.ReactionName(g.Emoji)
		if name == "" { // outside the set: no hub sends it, and its width is unknown
			name = "?"
		}
		mine := slices.Contains(g.Peers, me) && me != ""
		var text string
		if selected {
			who := make([]string, len(g.Peers))
			for j, peer := range g.Peers {
				who[j] = l.names.short(peer)
				if peer == me && me != "" {
					who[j] = "you"
				}
			}
			text = "[" + name + ": " + strings.Join(who, ", ") + "]"
		} else {
			mark := ""
			if mine {
				mark = "*"
			}
			text = fmt.Sprintf("[%s %d%s]", name, len(g.Peers), mark)
		}
		style := styleMuted
		if mine {
			style = styleCyan
		}
		sep := ""
		if i > 0 {
			sep = " "
		}
		room := l.width - used - len(sep)
		if i < len(msg.Reactions)-1 {
			room -= len(" +8 more") // what a cut after this chip needs
		}
		if lipgloss.Width(text) > room {
			if i > 0 {
				return append(row, chatSeg{fmt.Sprintf(" +%d more", len(msg.Reactions)-i), styleMuted})
			}
			text = ansi.Truncate(text, max(room, 0), "…")
		}
		used += len(sep) + lipgloss.Width(text)
		row = append(row, chatSeg{sep, styleMuted}, chatSeg{text, style})
	}
	return row
}

// window chooses the first message to draw: the newest screenful while
// following, otherwise wherever the view already is if the selection fits
// there. Working in whole messages draws only what shows, however long the
// thread.
func (s *chatScreen) window(n, picked, h int, height func(int) int) int {
	backFrom := func(i int) int {
		used := height(i)
		for i > 0 && used+height(i-1) <= h {
			i--
			used += height(i)
		}
		return i
	}
	if s.following() {
		return backFrom(n - 1)
	}
	top := min(s.top, picked)
	used := 0
	for i := top; i <= picked; i++ {
		used += height(i)
		if used > h {
			return backFrom(picked)
		}
	}
	return top
}

func (s *chatScreen) viewLog(m *Model, st *ThreadState, w, h int) string {
	id := m.CurrentThread
	switch {
	case id == "" && len(m.Data.Threads.Threads) == 0:
		return styleMuted.Render("no threads yet: :msg <peer> <text> starts one")
	case st == nil || st.Loading:
		return styleMuted.Render("loading")
	}
	if err := m.Data.Errs[threadPrefix+id]; err != nil && len(st.Messages) == 0 {
		return styleBad.Render(ansi.Truncate(Clean("could not load: "+diagText(err.Error())), w, "…"))
	}
	if len(st.Messages) == 0 {
		return styleMuted.Render("no messages yet")
	}
	l := s.newChatLog(m, st, w, h)
	picked := s.index(st)
	drawn := map[int][]chatRow{}
	rowsOf := func(i int) []chatRow {
		rows, ok := drawn[i]
		if !ok {
			rows = l.item(&st.Messages[i], i == picked)
			drawn[i] = rows
		}
		return rows
	}
	s.top = s.window(len(st.Messages), picked, h, func(i int) int { return len(rowsOf(i)) })
	lines := make([]string, 0, h)
	for i := s.top; i < len(st.Messages) && len(lines) < h; i++ {
		for _, row := range rowsOf(i) {
			lines = append(lines, row.render(w, i == picked))
		}
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

func chatPresence(state string) (string, lipgloss.Style) {
	switch state {
	case "working":
		return "work", styleWarn
	case "idle":
		return "idle", styleGood
	case "blocked":
		return "blkd", styleBad
	case "offline":
		return "off", styleMuted
	}
	return "unkn", styleMuted // anything unrecognised, and never offline (§8)
}

func chatReach(state string) (string, lipgloss.Style) {
	switch state {
	case "connected":
		return "conn", styleGood
	case "reachable":
		return "reach", styleGood
	case "unreachable":
		return "unrch", styleBad
	}
	return "unkn", styleMuted
}

// viewWho is the participants, the thread's files and, when there is room, the
// legends. Presence and reachability are separate columns (§8).
func (s *chatScreen) viewWho(m *Model, st *ThreadState, w, h int) string {
	row := chatRowOf(m, m.CurrentThread)
	if row == nil {
		return ""
	}
	const presenceW, reachW = 4, 5
	nameW := w - 2 - presenceW - 1 - reachW - 1
	peers := make(map[string]opapi.Peer, len(m.Data.Peers))
	for _, p := range m.Data.Peers {
		peers[p.Peer.PeerID] = p.Peer
	}
	names := chatNames(m)

	var files []opapi.ThreadFile
	if st != nil {
		files = st.Detail.Files
	}
	shownFiles := min(len(files), 6)
	filesRows := 0
	if shownFiles > 0 {
		filesRows = shownFiles + 2
	}
	participants := row.Thread.Participants
	maxFrom := max(len(participants)-1, 0)
	if s.whoFrom < 0 {
		s.whoFrom = 0
	}
	if s.whoFrom > maxFrom {
		s.whoFrom = maxFrom
	}
	participants = participants[s.whoFrom:]
	room := max(h-1-filesRows, 1)
	shownPeers := min(len(participants), room)
	if shownPeers < len(participants) {
		shownPeers = room - 1 // the last row says how many are left out
	}

	lines := []string{styleMuted.Render(fit("peer", 2+nameW) + " " + fit("pres", presenceW) + " reach")}
	for _, id := range participants[:shownPeers] {
		p := peers[id]
		presence, presenceStyle := chatPresence(p.Presence)
		reach, reachStyle := chatReach(p.Reachability)
		name := fit(ansi.Truncate(names.of(id), nameW, "…"), nameW)
		lines = append(lines, chatSenderStyle(id).Render("■")+" "+styleText.Render(name)+" "+presenceStyle.Render(fit(presence, presenceW))+" "+reachStyle.Render(reach))
	}
	if extra := len(participants) - shownPeers; extra > 0 {
		lines = append(lines, styleMuted.Render(fmt.Sprintf("+%d more", extra)))
	}
	if shownFiles > 0 {
		lines = append(lines, "", styleMuted.Render("files:"))
		for _, f := range files[len(files)-shownFiles:] {
			size := chatSize(f.Size)
			if f.Purged {
				size = "purged"
			}
			name := Clean(f.Name)
			if name == "" {
				name = chatShort(f.SHA256)
			}
			lines = append(lines, styleCyan.Render(ansi.Truncate(name, max(w-lipgloss.Width(size)-1, 1), "…"))+" "+styleMuted.Render(size))
		}
	}
	if g := row.Thread.Group; g != nil {
		lines = append(lines, "", styleMuted.Render("ad hoc group:"), styleText.Render(ansi.Truncate(Clean(*g), w, "…")))
	}
	legend := []string{
		"",
		styleMuted.Render("receipts:"),
		styleMuted.Render("· queued  › pushed"),
		styleAccepted.Render("✓ accepted"),
		styleGood.Render("✓✓ processed"),
		styleText.Render("● read") + "  " + styleBad.Render("✗ dead"),
		"",
		styleMuted.Render("priority:"),
		styleWarn.Render("! wake") + "  " + styleCyan.Render("~ wake_idle"),
		styleMuted.Render("  hold"),
	}
	if len(lines)+len(legend) <= h {
		lines = append(lines, legend...)
	}
	return strings.Join(lines, "\n")
}

// viewComposer is the last body row: the answer prompt, why a :msg failed, or
// the thread's label and the draft, with a byte counter once there is one.
func (s *chatScreen) viewComposer(m *Model, st *ThreadState, w int) string {
	if s.prompt != nil {
		return s.viewPrompt(w)
	}
	if s.msgErr != "" && m.Mode == ModeCommand {
		return fit(styleBad.Render(s.msgErr), w)
	}
	id := m.CurrentThread
	if id == "" {
		return fit(styleMuted.Render("no thread open"), w)
	}
	title := "thread"
	if row := chatRowOf(m, id); row != nil {
		title = chatTitleOf(row.Thread)
	}
	label := styleAccent.Render(ansi.Truncate(title, 24, "…")) + styleMuted.Render(" › ")
	d := s.draft(id)
	if d.reply != "" && st != nil {
		label += styleMuted.Render(chatReplyMark(m, chatNames(m), chatMessages(st), d.reply) + " ")
	}
	status := ""
	if n := len(d.text); n > 0 {
		status = chatSize(int64(n))
		if n > chatSpillAt {
			status += ", spills"
		}
	}
	if d.sending {
		status = "sending"
	}
	room := w - lipgloss.Width(label) - lipgloss.Width(status) - 2
	text := Clean(strings.ReplaceAll(d.text, "\n", "⏎"))
	var body string
	switch {
	case s.insert:
		if over := lipgloss.Width(text) + 1 - room; over > 0 { // the end being typed stays in view
			text = ansi.TruncateLeft(text, over+1, "…")
		}
		body = styleText.Render(text) + styleAccent.Render("█")
	case text != "":
		body = styleText.Render(ansi.Truncate(text, room, "…"))
	case st != nil && st.Detail.Row.Thread.ThreadID == id && !slices.Contains(st.Detail.Row.Thread.Participants, m.Data.Inbox.Me.PeerID):
		body = styleMuted.Render(ansi.Truncate("i to write: sending joins you to this thread", room, "…"))
	default:
		body = styleMuted.Render(ansi.Truncate("i to write, or :msg <peer> <text>", room, "…"))
	}
	return fit(fit(label+body, w-lipgloss.Width(status)-1)+" "+styleMuted.Render(status), w)
}

// viewPrompt names what the digit answers. The question gives way to the
// options, but never entirely: they mean nothing without it.
func (s *chatScreen) viewPrompt(w int) string {
	const head, gap = "answer: ", "  "
	options := make([]string, len(s.prompt.Options))
	for i, option := range s.prompt.Options {
		options[i] = fmt.Sprintf("%d %s", i+1, Clean(option))
	}
	listed := strings.Join(options, gap)
	room := max(w-len(head)-len(gap)-lipgloss.Width(listed), w/3)
	question := ansi.Truncate(Clean(s.prompt.Question), room, "…")
	return fit(styleWarn.Render(head)+styleText.Render(question+gap+listed), w)
}

// viewMatrix is one row per message and one column per recipient, showing each
// cell's display. Both directions scroll, so 64 recipients stay reachable.
func (s *chatScreen) viewMatrix(m *Model, st *ThreadState, w, h int) string {
	mx := st.Matrix
	names := chatNames(m)
	columns := max((w-2-chatMatrixDay-chatMatrixWho)/chatMatrixBox, 1)
	rows := max(h-3, 1)
	s.matrixX = min(max(s.matrixX, 0), max(len(mx.Recipients)-columns, 0))
	s.matrixY = min(max(s.matrixY, 0), max(len(mx.Rows)-rows, 0))
	shown := mx.Recipients[s.matrixX:min(s.matrixX+columns, len(mx.Recipients))]

	title := fmt.Sprintf("delivery matrix · recipients %d-%d of %d · rows %d-%d of %d",
		min(s.matrixX+1, len(mx.Recipients)), s.matrixX+len(shown), len(mx.Recipients),
		min(s.matrixY+1, len(mx.Rows)), min(s.matrixY+rows, len(mx.Rows)), len(mx.Rows))
	if len(mx.Rows) == 0 {
		return Pane(title, w, h, true, styleMuted.Render("no deliveries in this thread"))
	}
	var head strings.Builder
	head.WriteString(fit("time", chatMatrixDay) + fit("sender", chatMatrixWho))
	for _, id := range shown {
		head.WriteString(fit(ansi.Truncate(names.of(id), chatMatrixBox-1, "…"), chatMatrixBox))
	}
	lines := []string{styleMuted.Render(head.String())}
	for _, row := range mx.Rows[s.matrixY:min(s.matrixY+rows, len(mx.Rows))] {
		var line strings.Builder
		line.WriteString(styleMuted.Render(fit(chatClock(m, row.CreatedAt), chatMatrixDay)))
		line.WriteString(chatSenderStyle(row.Sender).Render(fit(ansi.Truncate(names.of(row.Sender), chatMatrixWho-1, "…"), chatMatrixWho)))
		for _, id := range shown {
			text, style := "-", styleMuted
			if i := slices.IndexFunc(row.Cells, func(c opapi.MatrixCell) bool { return c.Recipient == id }); i >= 0 {
				text = Clean(row.Cells[i].Display)
				_, style = chatReceipt(text)
			}
			line.WriteString(style.Render(fit(ansi.Truncate(text, chatMatrixBox-1, "…"), chatMatrixBox)))
		}
		lines = append(lines, line.String())
	}
	return Pane(title, w, h, true, strings.Join(lines, "\n"))
}

// viewReader shows a message's full text. The wrapped lines are kept, because
// a body can be a mebibyte and frames are not.
func (s *chatScreen) viewReader(w, h int) string {
	r := &s.reader
	if r.loading {
		return Pane(r.title, w, h, true, styleMuted.Render("loading"))
	}
	inner := w - 2
	if r.width != inner {
		body := strings.ReplaceAll(r.body, "\t", "    ")
		r.lines, r.width = strings.Split(ansi.Hardwrap(body, inner, true), "\n"), inner
	}
	r.rows = max(h-2, 1)
	r.top = min(max(r.top, 0), max(len(r.lines)-r.rows, 0))
	end := min(r.top+r.rows, len(r.lines))
	title := fmt.Sprintf("%s · lines %d-%d of %d", r.title, r.top+1, end, len(r.lines))
	return Pane(title, w, h, true, styleText.Render(strings.Join(r.lines[r.top:end], "\n")))
}
