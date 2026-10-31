package bot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

func TestRelayable(t *testing.T) {
	player := &discordgo.User{ID: "u"}
	cases := []struct {
		name string
		m    discordgo.Message
		want bool
	}{
		{"player", discordgo.Message{GuildID: "g", Author: player}, true},
		{"reply", discordgo.Message{GuildID: "g", Author: player, Type: discordgo.MessageTypeReply}, true},
		{"bot", discordgo.Message{GuildID: "g", Author: &discordgo.User{ID: "b", Bot: true}}, false},
		{"relay", discordgo.Message{GuildID: "g", Author: player, WebhookID: "w"}, false},
		{"added to thread", discordgo.Message{GuildID: "g", Author: player, Type: discordgo.MessageTypeRecipientAdd}, false},
		{"dm", discordgo.Message{Author: player}, false},
	}
	for _, c := range cases {
		if got := relayable(&c.m); got != c.want {
			t.Errorf("%s: relayable %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRelayContent(t *testing.T) {
	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{s: s}

	// Mentions become names, which other servers can read.
	m := &discordgo.Message{Content: "  hi <@42>, ready?  ", Mentions: []*discordgo.User{{ID: "42", Username: "pepa"}}}
	if got := b.relayContent(m); got != "hi @pepa, ready?" {
		t.Errorf("mentions: %q", got)
	}

	// Attachments come after the text as links and survive a long message.
	m = &discordgo.Message{Content: strings.Repeat("x", 3000), Attachments: []*discordgo.MessageAttachment{{URL: "https://cdn/a.png"}}}
	got := b.relayContent(m)
	if utf8.RuneCountInString(got) > maxContent || !strings.HasSuffix(got, "\nhttps://cdn/a.png") {
		t.Errorf("long message with attachment: %d characters, ends %q", utf8.RuneCountInString(got), got[len(got)-30:])
	}
	if got := b.relayContent(&discordgo.Message{Attachments: m.Attachments}); got != "https://cdn/a.png" {
		t.Errorf("attachment only: %q", got)
	}
	if got := b.relayContent(&discordgo.Message{}); got != "" {
		t.Errorf("sticker only: %q", got)
	}
}

func TestRelayAuthor(t *testing.T) {
	a := author{name: "Pepa_*", server: strings.Repeat("S", 100)}
	if got := a.webhookName(); utf8.RuneCountInString(got) > maxWebhookName || !strings.HasPrefix(got, "Pepa_* · SSS") {
		t.Errorf("webhook name %q", got)
	}
	if got := (author{name: "Pepa"}).webhookName(); got != "Pepa" {
		t.Errorf("webhook name without server %q", got)
	}
	got := a.prefixed(strings.Repeat("y", maxContent))
	if utf8.RuneCountInString(got) > maxContent || !strings.HasPrefix(got, `**Pepa\_\*** · *SSS`) {
		t.Errorf("prefixed %q…", got[:40])
	}
}

func TestEditModal(t *testing.T) {
	b := testBot(t)
	zone, _ := time.LoadLocation("Europe/Prague")
	start := time.Date(2026, 11, 7, 20, 30, 0, 0, zone)
	p := lfg.Post{ID: "abc123", DungeonID: "krao-cave", ModeID: "conquest-hard", MinGS: 2700, StartAt: start,
		Note: strings.Repeat("n", 300), Status: lfg.StatusOpen}

	for _, l := range i18n.Langs {
		m := b.editModal(p, zone, l)
		if len(m.CustomID) > 100 || len([]rune(m.Title)) > 45 {
			t.Fatalf("%s edit modal %q", l, m.CustomID)
		}
		values := map[string]string{}
		for _, c := range m.Components {
			ti := c.(discordgo.Label).Component.(discordgo.TextInput)
			values[ti.CustomID] = ti.Value
		}
		if values["gs"] != "2700" || values["note"] != p.Note || values["start"] != "07.11.2026 20:30" {
			t.Errorf("%s edit modal is filled in with %v", l, values)
		}
		if !strings.Contains(m.Components[0].(discordgo.Label).Description, "2") {
			t.Errorf("%s edit modal lacks the game's gear score hint", l)
		}
	}

	// The form reads its own start back unchanged.
	at, err := parseStart(startInput(start, zone), zone, start.Add(-time.Hour))
	if err != nil || !at.Equal(start) {
		t.Fatalf("start round trip: %v, %v", at, err)
	}
	if startInput(time.Time{}, zone) != "" {
		t.Fatal("no start shows as empty")
	}
}

func TestUseIcons(t *testing.T) {
	b := testBot(t)
	b.game.UseIcons(map[string]string{"tank": "<:lfg_tank_abc:1>", "cleric": "<:lfg_cleric_abc:2>"})
	if r, _ := b.game.Role("tank"); r.Emoji != "<:lfg_tank_abc:1>" {
		t.Errorf("tank emoji %q", r.Emoji)
	}
	if r, _ := b.game.Role("healer"); r.Emoji == "" || strings.HasPrefix(r.Emoji, "<") {
		t.Errorf("healer has no icon and keeps its emoji, got %q", r.Emoji)
	}
	if e := emoji(b.roleBullet("tank")); e == nil || e.ID != "1" || e.Name != "lfg_tank_abc" {
		t.Errorf("icon as a component emoji: %+v", e)
	}
	if got := b.choiceText("cleric", "healer"); !strings.Contains(got, "<:lfg_cleric_abc:2>") {
		t.Errorf("choice text %q", got)
	}
}
