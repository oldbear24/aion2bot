package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	b.game.UseIcons(map[string]string{"tank": "<:lfg_tank_abc:1>", "cleric": "<:lfg_cleric_abc:2>", "elyos": "<:lfg_elyos_abc:3>"})
	if got := b.factionTag("elyos"); got != "<:lfg_elyos_abc:3> Elyos" {
		t.Errorf("faction with an icon: %q", got)
	}
	if got := b.factionTag("asmodian"); got != "🌙 Asmodian" {
		t.Errorf("faction without an icon keeps its emoji: %q", got)
	}
	if opts := b.factionOptions("elyos"); opts[0].Emoji == nil || opts[0].Emoji.ID != "3" || !opts[0].Default {
		t.Errorf("faction menu option %+v", opts[0])
	}
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

func TestExcerpt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Need a **healer**?\n> anyone", "Need a **healer**? anyone"},
		{"# Big\n-# small\n- item", "Big small item"},
		{"see [the guide](https://example.com/guide) and https://example.com/x", "see the guide and <https://example.com/x>"},
		{"https://cdn.discordapp.com/attachments/1/2/a.png", "📎"},
		{"```go\ncode\n```", "`go code `"},
		{strings.Repeat("word ", 40), strings.TrimSpace(strings.Repeat("word ", 20)) + "…"},
		{strings.Repeat("a", 30) + " ||" + strings.Repeat("secret ", 20) + "||", strings.Repeat("a", 30) + "…"},
		{"https://example.com/" + strings.Repeat("x", 200), "🔗…"},
		{"", ""},
	}
	for _, c := range cases {
		if got := excerptOf(c.in); got != c.want {
			t.Errorf("excerpt of %q: %q, want %q", c.in, got, c.want)
		}
		if n := utf8.RuneCountInString(excerptOf(c.in)); n > maxExcerpt+1 {
			t.Errorf("excerpt of %q is %d characters", c.in, n)
		}
	}
}

func TestQuoteLine(t *testing.T) {
	link := "https://discord.com/channels/1234567890123456789/1234567890123456789/1234567890123456789"
	if got := quoteLine("Ann_[x]", "hi", link); got != "-# ↪ [Ann\\_x]("+link+"): hi" {
		t.Errorf("linked quote %q", got)
	}
	if got := quoteLine("Ann", "", ""); got != "-# ↪ **Ann**" {
		t.Errorf("quote without a link or text %q", got)
	}
	long := quoteLine(strings.Repeat("*", 100), excerptOf(strings.Repeat("ab ", 100)), link)
	if n := utf8.RuneCountInString(long); n > maxQuote {
		t.Errorf("quote of %d characters", n)
	}

	// The text of a relayed copy comes without the bot's own additions.
	webhook := &discordgo.Message{WebhookID: "w", Content: "-# ↪ **Bob**: earlier\n**Ann**: hello"}
	if got := relayedText(webhook); got != "**Ann**: hello" {
		t.Errorf("webhook copy text %q", got)
	}
	own := &discordgo.Message{Content: "-# ↪ **Bob**: earlier\n" + (author{name: "A*n_", server: "S\\x"}).prefixed("hello")}
	if got := relayedText(own); got != "hello" {
		t.Errorf("bot copy text %q", got)
	}
}

func TestRelayContentMedia(t *testing.T) {
	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{s: s}

	// Stickers come as pictures, or by name when Discord has none.
	m := &discordgo.Message{Content: "gg", StickerItems: []*discordgo.StickerItem{
		{ID: "1", FormatType: discordgo.StickerFormatTypePNG}, {ID: "2", FormatType: discordgo.StickerFormatTypeGIF},
		{ID: "3", Name: "Wumpus_wave", FormatType: discordgo.StickerFormatTypeLottie}}}
	want := "gg\nhttps://media.discordapp.net/stickers/1.png?size=160\nhttps://media.discordapp.net/stickers/2.gif?size=160\n🏷️ *Wumpus\\_wave*"
	if got := b.relayContent(m); got != want {
		t.Errorf("stickers: %q", got)
	}

	// A forwarded message comes as a quote, with its attachments.
	m = &discordgo.Message{MessageReference: &discordgo.MessageReference{Type: discordgo.MessageReferenceTypeForward},
		MessageSnapshots: []discordgo.MessageSnapshot{{Message: &discordgo.Message{Content: "line one\nline two",
			Attachments: []*discordgo.MessageAttachment{{URL: "https://cdn/b.png"}}}}}}
	if got := b.relayContent(m); got != "> line one\n> line two\nhttps://cdn/b.png" {
		t.Errorf("forward: %q", got)
	}
}

// relayDiscord answers the messages the bot posts with IDs that tell where
// they went: copy-<thread>-<n>.
func relayDiscord() *fakeDiscord {
	n := 0
	thread := regexp.MustCompile(`thread_id=(\w+)|^POST /channels/(\w+)/messages$`)
	return &fakeDiscord{answer: func(call string) (int, string) {
		switch {
		case strings.HasPrefix(call, "PATCH "):
			return http.StatusOK, "{}"
		case !strings.HasPrefix(call, "POST "):
			return http.StatusNoContent, ""
		}
		n++
		where := "x"
		if m := thread.FindStringSubmatch(call); m != nil {
			where = m[1] + m[2]
		}
		return http.StatusOK, fmt.Sprintf(`{"id":"copy-%s-%d"}`, where, n)
	}}
}

// relayBot has a post with its card and thread on three servers: g1 and g2
// relay through webhooks, g3 has none, so the bot posts there itself.
func relayBot(t *testing.T, f *fakeDiscord) (*Bot, lfg.Post) {
	t.Helper()
	b := goneBot(t, f)
	b.hookFailed = map[string]time.Time{"c3": time.Now()}
	b.s.State.User = &discordgo.User{ID: "bot", Bot: true}
	p := postWithCard(t, b)
	p, _ = b.store.AddMessages(p.ID, []lfg.MessageRef{{GuildID: "g2", ChannelID: "c2", MessageID: "m2"}, {GuildID: "g3", ChannelID: "c3", MessageID: "m3"}})
	for _, id := range []string{"m1", "m2", "m3"} {
		b.store.SetThread(p.ID, id)
	}
	b.store.SetWebhook("c1", lfg.Webhook{ID: "w1", Token: "t1"})
	b.store.SetWebhook("c2", lfg.Webhook{ID: "w2", Token: "t2"})
	return b, p
}

// posted is what the bot sent with calls starting so: the text, and the
// message it replies to.
type posted struct{ content, replyTo string }

func postedBy(t *testing.T, f *fakeDiscord, prefix string) []posted {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []posted
	for n, c := range f.calls {
		if !strings.HasPrefix(c, prefix) {
			continue
		}
		var body struct {
			Content   string `json:"content"`
			Reference *struct {
				MessageID string `json:"message_id"`
			} `json:"message_reference"`
		}
		if err := json.Unmarshal([]byte(f.bodies[n]), &body); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		p := posted{content: body.Content}
		if body.Reference != nil {
			p.replyTo = body.Reference.MessageID
		}
		out = append(out, p)
	}
	return out
}

func (f *fakeDiscord) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.bodies = nil, nil
}

// copyIn is the ID of the copy of a message in a thread.
func copyIn(t *testing.T, b *Bot, sourceID, threadID string) string {
	t.Helper()
	for _, c := range b.store.Relays(sourceID) {
		if c.ChannelID == threadID {
			return c.MessageID
		}
	}
	t.Fatalf("message %s has no copy in thread %s", sourceID, threadID)
	return ""
}

func TestRelayReplies(t *testing.T) {
	f := relayDiscord()
	b, _ := relayBot(t, f)
	ann := &discordgo.User{ID: "A", Username: "ann", GlobalName: "Ann"}
	bob := &discordgo.User{ID: "B", Username: "bob", GlobalName: "Bob"}
	cid := &discordgo.User{ID: "C", Username: "cid", GlobalName: "Cid"}

	a1 := &discordgo.Message{ID: "a1", GuildID: "g1", ChannelID: "m1", Author: ann, Content: "Need a **healer**?"}
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: a1})
	x2, x3 := copyIn(t, b, "a1", "m2"), copyIn(t, b, "a1", "m3")
	f.reset()

	// Bob answers the copy on his server. Ann's server shows what he
	// answers, linked to her message; g3 gets a real reply from the bot.
	b1 := &discordgo.Message{ID: "b1", GuildID: "g2", ChannelID: "m2", Author: bob, Type: discordgo.MessageTypeReply, Content: "I can come",
		MessageReference: &discordgo.MessageReference{MessageID: x2, ChannelID: "m2"},
		ReferencedMessage: &discordgo.Message{ID: x2, ChannelID: "m2", WebhookID: "w2", Content: "Need a **healer**?",
			Author: &discordgo.User{ID: "w2", Username: "Ann · One", Bot: true}}}
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: b1})
	quote := "-# ↪ [Ann](https://discord.com/channels/g1/m1/a1): Need a **healer**?"
	if got := postedBy(t, f, "POST /webhooks/w1/"); len(got) != 1 || got[0].content != quote+"\nI can come" {
		t.Fatalf("reply through the webhook on g1: %+v", got)
	}
	if got := postedBy(t, f, "POST /channels/m3/messages"); len(got) != 1 || got[0].content != "**Bob**: I can come" || got[0].replyTo != x3 {
		t.Fatalf("reply by the bot on g3: %+v", got)
	}

	// Edits keep the line.
	f.reset()
	b1.Content = "I can come at 8"
	b.onMessageUpdate(nil, &discordgo.MessageUpdate{Message: b1})
	b1.EditedTimestamp = &time.Time{}
	b.onMessageUpdate(nil, &discordgo.MessageUpdate{Message: b1})
	if got := postedBy(t, f, "PATCH /webhooks/w1/"); len(got) != 1 || got[0].content != quote+"\nI can come at 8" {
		t.Fatalf("edit through the webhook: %+v", got)
	}
	if got := postedBy(t, f, "PATCH /channels/m3/"); len(got) != 1 || got[0].content != "**Bob**: I can come at 8" {
		t.Fatalf("edit by the bot: %+v", got)
	}

	// Cid answers the bot's own copy on g3: the name it leads with isn't
	// quoted, and both other servers link to their own copy.
	f.reset()
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "c1", GuildID: "g3", ChannelID: "m3", Author: cid,
		Type: discordgo.MessageTypeReply, Content: "me too", MessageReference: &discordgo.MessageReference{MessageID: x3},
		ReferencedMessage: &discordgo.Message{ID: x3, ChannelID: "m3", Content: "**Ann** · *One*: Need a **healer**?",
			Author: &discordgo.User{ID: "bot", Bot: true}}}})
	if got := postedBy(t, f, "POST /webhooks/w2/"); len(got) != 1 ||
		got[0].content != "-# ↪ [Ann](https://discord.com/channels/g2/m2/"+x2+"): Need a **healer**?\nme too" {
		t.Fatalf("reply to the bot's copy: %+v", got)
	}

	// Replies to the bot's notes have nothing to show.
	f.reset()
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "b2", GuildID: "g2", ChannelID: "m2", Author: bob,
		Type: discordgo.MessageTypeReply, Content: "thanks", MessageReference: &discordgo.MessageReference{MessageID: "note"},
		ReferencedMessage: &discordgo.Message{ID: "note", ChannelID: "m2", Author: &discordgo.User{ID: "bot", Bot: true}}}})
	if got := postedBy(t, f, "POST /webhooks/w1/"); len(got) != 1 || got[0].content != "thanks" {
		t.Fatalf("reply to a note: %+v", got)
	}

	// A message that wasn't relayed to g3, its author being blocked there,
	// isn't quoted there either.
	if err := b.store.Block(lfg.Block{Scope: "g3", UserID: "A", Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "a2", GuildID: "g1", ChannelID: "m1", Author: ann, Content: "secret"}})
	f.reset()
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "b3", GuildID: "g2", ChannelID: "m2", Author: bob,
		Type: discordgo.MessageTypeReply, Content: "ok", MessageReference: &discordgo.MessageReference{MessageID: copyIn(t, b, "a2", "m2")},
		ReferencedMessage: &discordgo.Message{ID: copyIn(t, b, "a2", "m2"), ChannelID: "m2", WebhookID: "w2", Content: "secret",
			Author: &discordgo.User{ID: "w2", Username: "Ann · One", Bot: true}}}})
	if got := postedBy(t, f, "POST /channels/m3/messages"); len(got) != 1 || got[0].content != "**Bob**: ok" || got[0].replyTo != "" {
		t.Fatalf("reply to a message blocked on g3: %+v", got)
	}
	if got := postedBy(t, f, "POST /webhooks/w1/"); len(got) != 1 || !strings.HasPrefix(got[0].content, "-# ↪ [Ann](https://discord.com/channels/g1/m1/a2): secret\n") {
		t.Fatalf("the same reply on g1: %+v", got)
	}
}

// reactionCalls lists the bot's reaction calls, as "PUT m1/a1 👍" or
// "DELETE m2/copy 👍".
func reactionCalls(f *fakeDiscord) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		method, path, _ := strings.Cut(c, " ")
		parts := strings.Split(path, "/")
		if len(parts) == 8 && parts[5] == "reactions" && parts[7] == "@me" {
			out = append(out, method+" "+parts[2]+"/"+parts[4]+" "+parts[6])
		}
	}
	slices.Sort(out)
	return out
}

func TestMirrorReactions(t *testing.T) {
	f := relayDiscord()
	b, _ := relayBot(t, f)
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "a1", GuildID: "g1", ChannelID: "m1",
		Author: &discordgo.User{ID: "A", Username: "ann"}, Content: "ready?"}})
	x2, x3 := copyIn(t, b, "a1", "m2"), copyIn(t, b, "a1", "m3")
	react := func(user, guildID, channelID, messageID, emoji string, add bool) {
		t.Helper()
		f.reset()
		r := &discordgo.MessageReaction{UserID: user, GuildID: guildID, ChannelID: channelID, MessageID: messageID, Emoji: discordgo.Emoji{Name: emoji}}
		if add {
			b.onReactionAdd(nil, &discordgo.MessageReactionAdd{MessageReaction: r, Member: &discordgo.Member{User: &discordgo.User{ID: user}}})
		} else {
			b.onReactionRemove(nil, &discordgo.MessageReactionRemove{MessageReaction: r})
		}
	}
	expect := func(what string, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := reactionCalls(f); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
	}

	// Cid's 👍 on the copy on g3 shows on Ann's message and on g2's copy.
	react("C", "g3", "m3", x3, "👍", true)
	expect("first reaction", "PUT m1/a1 👍", "PUT m2/"+x2+" 👍")
	// Ann's 👍 on her own message: the bot is there already for Cid, and
	// now reacts on g3 for Ann.
	react("A", "g1", "m1", "a1", "👍", true)
	expect("second reaction", "PUT m3/"+x3+" 👍")
	// Cid takes his back: Ann's still counts for g2 and g3.
	react("C", "g3", "m3", x3, "👍", false)
	expect("one taken back", "DELETE m1/a1 👍")
	react("A", "g1", "m1", "a1", "👍", false)
	expect("all taken back", "DELETE m2/"+x2+" 👍", "DELETE m3/"+x3+" 👍")

	// The bot's own reactions, and other bots', aren't mirrored.
	react("bot", "g2", "m2", x2, "🎉", true)
	expect("the bot's reaction")
	f.reset()
	b.onReactionAdd(nil, &discordgo.MessageReactionAdd{Member: &discordgo.Member{User: &discordgo.User{ID: "O", Bot: true}},
		MessageReaction: &discordgo.MessageReaction{UserID: "O", GuildID: "g2", ChannelID: "m2", MessageID: x2, Emoji: discordgo.Emoji{Name: "🎉"}}})
	expect("another bot's reaction")

	// A player blocked on g3 reacts everywhere else; a custom emoji goes by
	// name and ID.
	if err := b.store.Block(lfg.Block{Scope: "g3", UserID: "D", Name: "Dan"}); err != nil {
		t.Fatal(err)
	}
	f.reset()
	b.onReactionAdd(nil, &discordgo.MessageReactionAdd{Member: &discordgo.Member{User: &discordgo.User{ID: "D"}},
		MessageReaction: &discordgo.MessageReaction{UserID: "D", GuildID: "g2", ChannelID: "m2", MessageID: x2, Emoji: discordgo.Emoji{Name: "lfg", ID: "77"}}})
	expect("blocked player", "PUT m1/a1 lfg:77")

	// A moderator on g1 clears Ann's message: the bot takes back what it
	// added on the other servers for the reactions there, and forgets them.
	react("B", "g2", "m2", x2, "👍", true)
	expect("reaction before clearing", "PUT m1/a1 👍", "PUT m3/"+x3+" 👍")
	f.reset()
	b.onReactionRemoveAll(nil, &discordgo.MessageReactionRemoveAll{MessageReaction: &discordgo.MessageReaction{GuildID: "g1", ChannelID: "m1", MessageID: "a1"}})
	expect("cleared on g1", "DELETE m3/"+x3+" 👍")
	if got := b.store.Reactions("a1", ""); len(got) != 0 {
		t.Errorf("reactions kept after clearing: %+v", got)
	}

	// Reactions on messages that weren't relayed are left alone.
	react("C", "g1", "m1", "note", "👍", true)
	expect("not relayed")
}
