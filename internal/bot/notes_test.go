package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// noteParts flattens a note into its texts and the custom IDs of its
// buttons.
func noteParts(t *testing.T, c discordgo.MessageComponent) (texts string, buttons []string) {
	t.Helper()
	if _, ok := c.(discordgo.Container); !ok {
		t.Fatalf("note is a %T, not a container", c)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, c := range v {
				walk(c)
			}
		case map[string]any:
			switch v["type"] {
			case float64(discordgo.TextDisplayComponent):
				sb.WriteString(v["content"].(string) + "\n")
			case float64(discordgo.ButtonComponent):
				buttons = append(buttons, v["custom_id"].(string))
			}
			for _, key := range []string{"components", "accessory"} {
				if child, ok := v[key]; ok && child != nil {
					walk(child)
				}
			}
		}
	}
	walk(tree)
	return sb.String(), buttons
}

// notePost has a leader from g1 and an applicant from g2, neither of whom
// is a member of the other server.
func notePost() (lfg.Post, lfg.Player) {
	start := time.Now().Add(time.Hour)
	p := lfg.Post{
		ID: "p1", LeaderID: "L", LeaderName: "Lea", LeaderChar: "LeaInGame", LeaderClass: "cleric", LeaderRole: "healer",
		GuildID: "g1", GuildName: "One", DungeonID: "krao-cave", ModeID: "conquest", Status: lfg.StatusOpen, Missing: 2,
		StartAt: start, LookingFor: []string{"tank", "dps"},
		Accepted: []lfg.Player{{UserID: "T", Name: "Tom", ClassID: "templar", RoleID: "tank", GuildID: "g1"}},
	}
	bob := lfg.Player{UserID: "B", Name: "Bob", ClassID: "sorcerer", RoleID: "dps", GS: 2500, GuildID: "g2", GuildName: "Two", Char: "Bobby"}
	return p, bob
}

func TestThreadNotes(t *testing.T) {
	b := testBot(t)
	p, bob := notePost()
	for _, l := range i18n.Langs {
		// The leader is linked, and so pinged, on their own server; Bob
		// shows by name there, since he isn't a member.
		texts, buttons := noteParts(t, b.joinNote(p, bob, false, "g1", l))
		for _, want := range []string{"### ✋", "<@L>", "**Bob**", "`Bobby`", "GS " + l.GS(2500), "*Two*"} {
			if !strings.Contains(texts, want) {
				t.Errorf("%s join note misses %q:\n%s", l, want, texts)
			}
		}
		if len(buttons) != 1 || buttons[0] != customID(actManage, p.ID) {
			t.Errorf("%s join note buttons %v, want Manage", l, buttons)
		}
		texts, _ = noteParts(t, b.joinNote(p, bob, false, "g2", l))
		if !strings.Contains(texts, "<@B>") || !strings.Contains(texts, "**Lea**") || strings.Contains(texts, "*Two*") {
			t.Errorf("%s join note on Bob's server:\n%s", l, texts)
		}
		if texts, _ = noteParts(t, b.joinNote(p, bob, true, "g1", l)); !strings.Contains(texts, "### 🪑") {
			t.Errorf("%s reserve note:\n%s", l, texts)
		}

		for _, promoted := range []bool{false, true} {
			texts, _ = noteParts(t, b.welcomeNote(p, bob, promoted, "g2", l))
			for _, want := range []string{"### 🎉", "<@B>", "**Lea**", b.bringing(p, bob), startText(p.StartAt, "", l), "`LeaInGame`"} {
				if !strings.Contains(texts, want) {
					t.Errorf("%s welcome note (promoted %v) misses %q:\n%s", l, promoted, want, texts)
				}
			}
		}

		// The reminder lists the party by role: Tom tanks, Lea heals.
		texts, _ = noteParts(t, b.reminderNote(p, "g1", l))
		tom, lea := strings.Index(texts, "<@T>"), strings.Index(texts, "<@L>")
		if !strings.Contains(texts, "### ⏰") || tom < 0 || lea < tom || !strings.Contains(texts, "`LeaInGame`") {
			t.Errorf("%s reminder:\n%s", l, texts)
		}

		after := p
		after.MinGS, after.Note = 2600, "bring *food*"
		texts, _ = noteParts(t, b.changesNote(p, after, "g1", l))
		if !strings.Contains(texts, "### ✏️") || !strings.Contains(texts, l.GS(2600)+"+") || !strings.Contains(texts, `bring \*food\*`) ||
			strings.Contains(texts, l.T("card.start")) {
			t.Errorf("%s changes note shows only what changed:\n%s", l, texts)
		}
	}
}

func TestThreadNotesFitDiscordLimits(t *testing.T) {
	b := iconBot(t)
	p := busyPost(b)
	p.StartAt = time.Now().Add(time.Hour)
	pl := p.Applicants[0]
	for _, l := range i18n.Langs {
		notes := map[string]discordgo.MessageComponent{
			"join":     b.joinNote(p, pl, false, "g1", l),
			"reserve":  b.joinNote(p, pl, true, "g1", l),
			"accepted": b.welcomeNote(p, pl, false, "g1", l),
			"promoted": b.welcomeNote(p, pl, true, "g1", l),
			"reminder": b.reminderNote(p, "g1", l),
			"changes":  b.changesNote(lfg.Post{}, p, "g1", l),
		}
		for name, note := range notes {
			count, textLen := measure(t, note)
			if count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s note: %d components, %d characters", l, name, count, textLen)
			}
			texts, _ := noteParts(t, note)
			if strings.Contains(texts, "%!") || strings.Contains(texts, "thread.") {
				t.Errorf("%s %s note has a broken text:\n%s", l, name, texts)
			}
		}
	}
}
