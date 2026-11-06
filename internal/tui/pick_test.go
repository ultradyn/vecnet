package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPickerConfirmCancelAndClick(t *testing.T) {
	choices := []ProfileChoice{
		{Name: "operator", Hub: "http://127.0.0.1:45752"},
		{Name: "fly", Hub: "https://vecnet.fly.dev"},
	}
	p := newPick(choices)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := p.View().Content
	plain := strip(view)
	if !strings.Contains(plain, "127.0.0.1:45752 local") || !strings.Contains(plain, "vecnet.fly.dev") {
		t.Fatalf("list:\n%s", plain)
	}
	if strings.Contains(plain, "vn_operator_") || strings.Contains(plain, "secret") {
		t.Fatalf("picker drew a secret:\n%s", plain)
	}
	if !strings.Contains(plain, "Shift+drag selects text") {
		t.Fatal("picker help line missing")
	}

	p.Update(keyPress("j"))
	p.Update(keyPress("enter"))
	if !p.confirm || p.choices[p.cursor].Name != "fly" {
		t.Fatalf("enter did not confirm fly: %+v", p.result)
	}
	plain = strip(p.View().Content)
	if !strings.Contains(plain, "run fly for this session") || !strings.Contains(plain, "make this the default operator_profile?") {
		t.Fatalf("confirm:\n%s", plain)
	}
	p.Update(keyPress("esc"))
	if p.confirm || p.result.Cancelled {
		t.Fatal("esc on confirm should return to the list")
	}

	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, cmd := p.Update(keyPress("n"))
	if cmd == nil || p.result.MakeDefault || p.result.Name != "fly" {
		t.Fatalf("n: %+v", p.result)
	}

	p = newPick(choices)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.View()
	clickPick(t, p, "0")
	if !p.confirm || p.cursor != 0 {
		t.Fatal("click did not confirm the first row")
	}
	p.View()
	_, cmd = p.Update(clickMsg(t, p, "y"))
	if cmd == nil || !p.result.MakeDefault || p.result.Name != "operator" {
		t.Fatalf("click y: %+v", p.result)
	}

	p = newPick(choices)
	_, cmd = p.Update(keyPress("q"))
	if cmd == nil || !p.result.Cancelled {
		t.Fatal("q did not cancel")
	}
}

func strip(s string) string { return ansi.Strip(s) }

func clickPick(t *testing.T, p *pickModel, id string) {
	t.Helper()
	p.Update(clickMsg(t, p, id))
}

func clickMsg(t *testing.T, p *pickModel, id string) tea.MouseClickMsg {
	t.Helper()
	p.View()
	for i := len(p.hits) - 1; i >= 0; i-- {
		if p.hits[i].id == id {
			h := p.hits[i]
			return tea.MouseClickMsg{X: h.x, Y: h.y, Button: tea.MouseLeft}
		}
	}
	t.Fatalf("no pick hit %s", id)
	return tea.MouseClickMsg{}
}
