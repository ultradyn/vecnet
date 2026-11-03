package envelope_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func TestReactionTableMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/reactions.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Emoji, Name, Priority string }
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 || len(envelope.ReactionNames) != 8 {
		t.Fatal("want eight reactions and names")
	}
	var emojis, labels []string
	for i, row := range rows {
		emojis = append(emojis, row.Emoji)
		labels = append(labels, row.Emoji+" "+row.Name)
		if envelope.ReactionEmoji[i] != row.Emoji || envelope.ReactionNames[row.Name] != row.Emoji || envelope.DefaultReactionPriority(row.Emoji) != row.Priority || envelope.ReactionName(row.Emoji) != row.Name || envelope.ReactionIndex(row.Emoji) != i {
			t.Errorf("reaction %d differs from fixture: %+v", i, row)
		}
	}
	if got := envelope.ReactionList(); got != strings.Join(labels, ", ") {
		t.Errorf("list = %q", got)
	}
	if envelope.ReactionIndex("🙂") != -1 || envelope.ReactionName("🙂") != "" {
		t.Fatal("unknown emoji has a name or index")
	}
	var schema struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(envelope.SchemaJSON(), &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Defs["reactionEmoji"].Enum, emojis) {
		t.Fatalf("schema enum = %v, want %v", schema.Defs["reactionEmoji"].Enum, emojis)
	}
}

func TestCanonicalEmoji(t *testing.T) {
	for _, emoji := range []string{"👍", "👎", "❤️", "😂", "👀", "✅", "❓", "❗", "❤"} {
		want := emoji
		if emoji == "❤" {
			want = "❤️"
		}
		if got, ok := envelope.CanonicalEmoji(emoji); !ok || got != want {
			t.Errorf("%q = %q, %v", emoji, got, ok)
		}
	}
	for _, bad := range []string{"👍🏽", "🙂", "check", "", "✅́́", "\u202e✅"} {
		if got, ok := envelope.CanonicalEmoji(bad); ok || got != "" {
			t.Errorf("accepted %q: %q", bad, got)
		}
	}
}

func TestParseReactionNames(t *testing.T) {
	for input, want := range map[string]string{"+1": "👍", "CHECK": "✅", "q": "❓", "bang": "❗", "❤": "❤️", "👀": "👀"} {
		if got, ok := envelope.ParseReaction(input); !ok || got != want {
			t.Errorf("%q = %q, %v", input, got, ok)
		}
	}
	if got, ok := envelope.ParseReaction("smile"); ok || got != "" {
		t.Fatal("accepted smile")
	}
}
