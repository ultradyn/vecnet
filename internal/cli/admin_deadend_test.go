package cli_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

func archiveCLIState(t *testing.T, env *testhub.Env, id string) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT archived_at IS NOT NULL FROM threads WHERE thr_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOperatorDeadEndUsage: the new admin subcommands reject a bad invocation
// before any hub call, and vecnet help names each of them.
func TestOperatorDeadEndUsage(t *testing.T) {
	u := newUser(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"peer", "disable"}, "peer disable"},
		{[]string{"peer", "enable", "peer_x", "extra"}, "peer enable"},
		{[]string{"groups", "unarchive"}, "groups unarchive"},
		{[]string{"groups", "unarchive", "grp_x", "grp_y"}, "groups unarchive"},
		{[]string{"thread", "archive"}, "thread archive"},
		{[]string{"thread", "unarchive", "thr_x", "extra"}, "thread unarchive"},
		{[]string{"thread", "archive", "thr_x", "--limit", "2"}, "thread archive"},
		{[]string{"thread", "budget", "thr_x"}, "thread budget"},
		{[]string{"thread", "budget", "thr_x", "-1"}, "thread budget"},
		{[]string{"thread", "budget", "thr_x", "nope"}, "thread budget"},
		{[]string{"team", "members"}, "team members"},
		{[]string{"team", "members", "team_x"}, "team members"},
		{[]string{"team", "members", "team_x", "extra", "--add", "peer_x"}, "team members"},
		{[]string{"team", "list", "extra"}, "team list"},
	}
	for _, tc := range cases {
		code, _, errs := u.run(tc.args...)
		if code != 1 || !strings.Contains(errs, tc.want) {
			t.Errorf("vecnet %s: exit %d stderr %q, want 1 containing %q", strings.Join(tc.args, " "), code, errs, tc.want)
		}
	}
	out := u.mustRun("--help")
	for _, want := range []string{
		"peer disable <id>",
		"peer enable <id>",
		"groups unarchive",
		"thread archive <id>",
		"thread unarchive <id>",
		"thread budget <id> <n>",
		"team list",
		"team members <id> --add",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

// TestOperatorDeadEndCommands drives the operator CLI against a local hub:
// peer disable/enable, group unarchive, thread budget, and team list/members.
func TestOperatorDeadEndCommands(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	a, aSec := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")

	if got := op.mustRun("peer", "disable", a); got != "disabled "+a+"\n" {
		t.Fatalf("peer disable = %q", got)
	}
	if disabled := peerDisabled(t, env, a); disabled != 1 {
		t.Fatalf("disabled = %d, want 1", disabled)
	}
	if got := op.mustRun("--json", "peer", "enable", a); strings.TrimSpace(got) != `{"ok":true}` {
		t.Fatalf("peer enable --json = %q", got)
	}
	if disabled := peerDisabled(t, env, a); disabled != 0 {
		t.Fatalf("disabled = %d after enable, want 0", disabled)
	}

	sender := profileUser(t, env.URL, aSec)
	sent := sender.send(b, "hello budget", "--hold")
	if got := op.mustRun("thread", "budget", sent.ThreadID, "7"); got != "budget "+sent.ThreadID+" 7\n" {
		t.Fatalf("thread budget = %q", got)
	}
	var budget sql.NullInt64
	if err := env.Hub.DB.QueryRow(`SELECT budget_remaining FROM threads WHERE thr_id = ?`, sent.ThreadID).Scan(&budget); err != nil || !budget.Valid || budget.Int64 != 7 {
		t.Fatalf("budget_remaining = %v, %v; want 7", budget, err)
	}
	if got := op.mustRun("thread", "archive", sent.ThreadID); got != "archived "+sent.ThreadID+"\n" {
		t.Fatalf("thread archive=%q", got)
	}
	if n := archiveCLIState(t, env, sent.ThreadID); n != 1 {
		t.Fatal("CLI did not archive thread")
	}
	op.mustRun("thread", "archive", sent.ThreadID)
	if got := op.mustRun("--json", "thread", "unarchive", sent.ThreadID); strings.TrimSpace(got) != `{"ok":true}` {
		t.Fatalf("thread unarchive=%q", got)
	}
	if n := archiveCLIState(t, env, sent.ThreadID); n != 0 {
		t.Fatal("CLI did not unarchive thread")
	}
	if out := sender.mustRun("thread", sent.ThreadID); !strings.Contains(out, "hello budget") {
		t.Fatalf("thread read = %q, want the message", out)
	}

	grp := strings.TrimSpace(op.mustRun("groups", "create", a, b))
	if !strings.HasPrefix(grp, "grp_") {
		t.Fatalf("groups create = %q", grp)
	}
	n, err := env.Hub.ArchiveIdleGroups(context.Background(), time.Now().Add(31*24*time.Hour))
	if err != nil || n < 1 {
		t.Fatalf("ArchiveIdleGroups = %d, %v; want at least 1", n, err)
	}
	if listed := op.mustRun("groups", "list"); !strings.Contains(listed, grp+" ") || !strings.Contains(listed, " archived") {
		t.Fatalf("groups list before unarchive = %q, want %s archived", listed, grp)
	}
	if got := op.mustRun("groups", "unarchive", grp); got != "unarchived "+grp+"\n" {
		t.Fatalf("groups unarchive = %q", got)
	}
	if listed := op.mustRun("groups", "list"); !strings.Contains(listed, grp+" ") || strings.Contains(listed, " archived") {
		t.Fatalf("groups list after unarchive = %q, want %s and no archived mark", listed, grp)
	}

	raw := op.mustRun("--json", "team", "add", "--name", "Ops", "--members", a)
	var added struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal([]byte(raw), &added); err != nil || !strings.HasPrefix(added.TeamID, "team_") {
		t.Fatalf("team add --json = %q, %v", raw, err)
	}
	if got := op.mustRun("team", "members", added.TeamID, "--add", b+","+c); got != "members added to "+added.TeamID+"\n" {
		t.Fatalf("team members --add = %q", got)
	}
	// A second --add of one existing member is the same POST; the hub keeps
	// members already on the team.
	if got := op.mustRun("team", "members", "--add", b, "--add", a, added.TeamID); got != "members added to "+added.TeamID+"\n" {
		t.Fatalf("team members repeated --add = %q", got)
	}
	listed := op.mustRun("team", "list")
	if !strings.Contains(listed, added.TeamID) || !strings.Contains(listed, "Ops") {
		t.Fatalf("team list = %q, want %s Ops", listed, added.TeamID)
	}
	for _, id := range []string{a, b, c} {
		if !strings.Contains(listed, id) {
			t.Fatalf("team list = %q, want member %s", listed, id)
		}
	}
	j := op.mustRun("--json", "team", "list")
	var teams []struct {
		TeamID  string   `json:"team_id"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := json.Unmarshal([]byte(j), &teams); err != nil {
		t.Fatalf("team list --json = %q: %v", j, err)
	}
	if len(teams) != 1 || teams[0].TeamID != added.TeamID || teams[0].Name != "Ops" || !sameMembers(teams[0].Members, []string{a, b, c}) {
		t.Fatalf("team list --json = %+v, want one Ops team with %s %s %s", teams, a, b, c)
	}

	peer := profileUser(t, env.URL, aSec)
	for _, args := range [][]string{
		{"peer", "disable", b},
		{"groups", "unarchive", grp},
		{"thread", "budget", sent.ThreadID, "9"},
		{"team", "list"},
		{"team", "members", added.TeamID, "--add", c},
	} {
		code, _, errs := peer.run(args...)
		if code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
			t.Errorf("vecnet %s as a peer = %d %q, want 2 forbidden", strings.Join(args, " "), code, errs)
		}
	}
}

func peerDisabled(t *testing.T, env *testhub.Env, peer string) int {
	t.Helper()
	var disabled int
	if err := env.Hub.DB.QueryRow(`SELECT disabled FROM peers WHERE peer_id = ?`, peer).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	return disabled
}

func sameMembers(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
