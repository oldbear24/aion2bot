package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const (
	missingAccess = `{"code":50001,"message":"Missing Access"}`
	unknownMsg    = `{"code":10008,"message":"Unknown Message"}`
	unknownChan   = `{"code":10003,"message":"Unknown Channel"}`
	invalidForm   = `{"code":50035,"message":"Invalid Form Body"}`
)

// fakeDiscord stands in for Discord's REST API: it records every call, and
// answers with what answer says, or else with success.
type fakeDiscord struct {
	mu     sync.Mutex
	calls  []string
	bodies []string
	answer func(call string) (status int, body string)
	// answerSent answers instead of answer when set, knowing what the bot
	// sent with the call.
	answerSent func(call, sent string) (status int, body string)
}

func (f *fakeDiscord) RoundTrip(r *http.Request) (*http.Response, error) {
	var sent []byte
	if r.Body != nil {
		sent, _ = io.ReadAll(r.Body)
	}
	_, path, _ := strings.Cut(r.URL.Path, "/api/v")
	_, path, _ = strings.Cut(path, "/")
	call := r.Method + " /" + path
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.bodies = append(f.bodies, string(sent))
	status, body := http.StatusOK, "{}"
	switch {
	case f.answerSent != nil:
		status, body = f.answerSent(call, string(sent))
	case f.answer != nil:
		status, body = f.answer(call)
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (f *fakeDiscord) made(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

// goneBot talks to a fake Discord and keeps a real database, with server g1
// set up in Czech.
func goneBot(t *testing.T, f *fakeDiscord) *Bot {
	t.Helper()
	b := testBot(t)
	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	s.Client = &http.Client{Transport: f}
	b.s = s
	b.syncs = map[string]*syncState{}
	b.store, err = lfg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.store.Close() })
	if err := b.store.SetGuild(lfg.GuildConfig{GuildID: "g1", GuildName: "One", ChannelID: "c1", Lang: "cs"}); err != nil {
		t.Fatal(err)
	}
	return b
}

// postWithCard stores a post with its card up on server g1.
func postWithCard(t *testing.T, b *Bot) lfg.Post {
	t.Helper()
	p, _ := notePost()
	p.CreatedAt, p.ExpiresAt = time.Now(), time.Now().Add(time.Hour)
	p, err := b.store.CreatePost(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := b.store.AddMessages(p.ID, []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}})
	if !ok {
		t.Fatal("card not recorded")
	}
	return p
}

// click is the interaction someone's click on a message sends. The message
// comes the way Discord returns it, so parsed from JSON.
func click(t *testing.T, customID string, flags discordgo.MessageFlags, locale string, comps []discordgo.MessageComponent) *discordgo.InteractionCreate {
	t.Helper()
	sent, err := json.Marshal(comps)
	if err != nil {
		t.Fatal(err)
	}
	return clickJSON(t, customID, flags, locale, string(sent))
}

// clickJSON is click on a message whose components are given as JSON.
func clickJSON(t *testing.T, customID string, flags discordgo.MessageFlags, locale, comps string) *discordgo.InteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"9","application_id":"8","type":3,"token":"tok","guild_id":"g1","channel_id":"c1","locale":%q,
		"member":{"user":{"id":"someone","username":"someone"},"permissions":"0"},
		"message":{"id":"m1","channel_id":"c1","flags":%d,"components":%s},
		"data":{"custom_id":%q,"component_type":2}}`, locale, flags, comps, customID)
	var ic discordgo.InteractionCreate
	if err := json.Unmarshal([]byte(raw), &ic); err != nil {
		t.Fatal(err)
	}
	return &ic
}

const callback = "POST /interactions/9/tok/callback"

// answers lists what the bot answered the click with.
func answers(f *fakeDiscord) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, c := range f.calls {
		if c == callback {
			out = append(out, f.bodies[n])
		}
	}
	return out
}

func TestSyncForgetsACardOnlyWhenItIsLostForGood(t *testing.T) {
	for _, c := range []struct {
		name     string
		status   int
		body     string
		onServer bool
		forgets  bool
	}{
		{"kept out of the channel", http.StatusForbidden, missingAccess, true, false},
		{"off the server", http.StatusForbidden, missingAccess, false, true},
		{"card deleted", http.StatusNotFound, unknownMsg, true, true},
		{"channel deleted", http.StatusNotFound, unknownChan, true, true},
		{"Discord having trouble", http.StatusInternalServerError, "{}", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeDiscord{answer: func(string) (int, string) { return c.status, c.body }}
			b := goneBot(t, f)
			if c.onServer {
				if err := b.s.State.GuildAdd(&discordgo.Guild{ID: "g1"}); err != nil {
					t.Fatal(err)
				}
			}
			p := postWithCard(t, b)
			b.syncPost(p.ID)
			if !f.made("PATCH /channels/c1/messages/m1") {
				t.Fatalf("the card wasn't edited: %v", f.calls)
			}
			after, _ := b.store.Post(p.ID)
			if forgot := !after.InGuild("g1"); forgot != c.forgets {
				t.Errorf("forgot the card: %v, want %v", forgot, c.forgets)
			}
		})
	}
}

// A card the bot is kept out of for a while still follows its post once the
// bot is let back in, and goes when the post does.
func TestACardFollowsItsPostAfterALossOfAccess(t *testing.T) {
	keptOut := true
	f := &fakeDiscord{answer: func(string) (int, string) {
		if keptOut {
			return http.StatusForbidden, missingAccess
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	if err := b.s.State.GuildAdd(&discordgo.Guild{ID: "g1"}); err != nil {
		t.Fatal(err)
	}
	p := postWithCard(t, b)
	b.syncPost(p.ID)

	keptOut = false
	if _, err := b.store.UpdatePost(p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	f.calls, f.bodies = nil, nil
	b.syncPost(p.ID)
	if !f.made("PATCH /channels/c1/messages/m1") {
		t.Errorf("the card no longer follows its post: %v", f.calls)
	}

	b.deleteEnded(time.Now().Add(time.Minute))
	if !f.made("DELETE /channels/c1/messages/m1") {
		t.Errorf("the post went without its card: %v", f.calls)
	}
	if _, ok := b.store.Post(p.ID); ok {
		t.Error("the post wasn't deleted")
	}
}

// A card outlives its post when the bot couldn't delete it, or its database
// was replaced. Whatever is clicked on it, it stops looking open for everyone,
// keeps the party list, and offers to put the post back.
func TestClickOnACardLeftBehindRetiresIt(t *testing.T) {
	for _, action := range []string{actJoin, actLeave, actManage} {
		t.Run(action, func(t *testing.T) {
			f := &fakeDiscord{answer: func(string) (int, string) { return http.StatusNoContent, "" }}
			b := goneBot(t, f)
			// With the icons on, as in production.
			b.icons = newIconSet(map[string]string{iconPrefix + "join": "<:ui_join:123>"})
			gone, _ := notePost()
			gone.ID = "gadrd3"
			gone.ExpiresAt = time.Now().Add(72 * time.Hour)
			card := b.postCard(gone, "g1", "role1", i18n.Czech)

			b.onInteraction(nil, click(t, customID(action, gone.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card))

			got := answers(f)
			if len(got) != 1 {
				t.Fatalf("answered %d times: %v", len(got), f.calls)
			}
			var answer struct {
				Type discordgo.InteractionResponseType `json:"type"`
				Data struct {
					Flags           discordgo.MessageFlags            `json:"flags"`
					AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(got[0]), &answer); err != nil {
				t.Fatal(err)
			}
			if answer.Type != discordgo.InteractionResponseUpdateMessage {
				t.Fatalf("answered with type %d, want the card changed:\n%s", answer.Type, got[0])
			}
			if answer.Data.Flags != discordgo.MessageFlagsIsComponentsV2 {
				t.Errorf("flags %d", answer.Data.Flags)
			}
			if a := answer.Data.AllowedMentions; a == nil || len(a.Parse)+len(a.Roles)+len(a.Users) != 0 {
				t.Errorf("the change can ping: %+v", a)
			}
			if n := strings.Count(got[0], `"disabled":true`); n != 2 || strings.Count(got[0], `"disabled":false`) != 1 {
				t.Errorf("%d buttons off, want Join and Leave:\n%s", n, got[0])
			}
			if strings.Contains(got[0], customID(actManage, gone.ID)) {
				t.Errorf("Manage is still on the card:\n%s", got[0])
			}
			if !strings.Contains(got[0], fmt.Sprintf(`"accent_color":%d`, colorClosed)) {
				t.Errorf("the card isn't grey:\n%s", got[0])
			}
			// In the server's language, whoever clicked; the party stays and
			// the expiry goes.
			for _, want := range []string{i18n.Czech.T("card.gone", gone.ID), i18n.Czech.T("manage.reopen"), "<@L>", "<@T>", "<@&role1>",
				customID(actJoin, gone.ID), customID(actRestore, gone.ID)} {
				raw, _ := json.Marshal(want)
				if !strings.Contains(got[0], strings.Trim(string(raw), `"`)) {
					t.Errorf("the card lost %s:\n%s", want, got[0])
				}
			}
			if strings.Contains(got[0], "vyprší") {
				t.Errorf("the card still says when it expires:\n%s", got[0])
			}
		})
	}
}

// A card goes back to Discord the way Discord sent it, with the IDs it gave
// the components and all it knows about the picture.
func TestRetiringACardKeepsWhatDiscordSent(t *testing.T) {
	var m discordgo.Message
	err := json.Unmarshal([]byte(`{"components":[{"type":10,"id":1,"content":"<@&role1>"},
		{"type":17,"id":2,"accent_color":3066993,"spoiler":false,"components":[
			{"type":9,"id":3,"components":[{"type":10,"id":4,"content":"## Dungeon"}],"accessory":{"type":11,"id":5,"description":null,"spoiler":false,
				"media":{"id":"77","url":"https://example.com/dungeon.png","proxy_url":"https://media.discordapp.net/x","width":64,"height":64,
					"placeholder":"abc","placeholder_version":1,"content_type":"image/png","loading_state":2,"flags":0}}},
			{"type":14,"id":6,"divider":true,"spacing":1},
			{"type":10,"id":7,"content":"the party"},
			{"type":1,"id":8,"components":[
				{"type":2,"id":9,"style":3,"label":"Join","custom_id":"lfg:join:gadrd3","emoji":{"id":"11","name":"ui_join","animated":false}},
				{"type":2,"id":10,"style":2,"label":"Leave","custom_id":"lfg:leave:gadrd3"},
				{"type":2,"id":11,"style":2,"label":"Manage","custom_id":"lfg:manage:gadrd3"}]},
			{"type":10,"id":12,"content":"the footer"}]}]}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	b := testBot(t)
	if b.retireCard(m.Components, "another", i18n.English) {
		t.Error("took the card of another post for this one's")
	}
	if !b.retireCard(m.Components, "gadrd3", i18n.English) {
		t.Fatal("not taken for the post's card")
	}
	raw, err := json.Marshal(m.Components)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"content":"\u003c@\u0026role1\u003e"`, `"content":"## Dungeon"`, `"content":"the party"`, `"url":"https://example.com/dungeon.png"`,
		`"divider":true`, `"name":"ui_join","id":"11"`, `"custom_id":"lfg:restore:gadrd3"`,
		fmt.Sprintf(`"accent_color":%d`, colorClosed),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("retired card lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "the footer") || strings.Count(got, `"disabled":true`) != 2 {
		t.Errorf("retired card:\n%s", got)
	}
}

// Kept out of the channel until its post is deleted, the bot leaves the card
// behind; the first click on it retires it.
func TestACardLeftBehindByALastingLossOfAccess(t *testing.T) {
	f := &fakeDiscord{answer: func(call string) (int, string) {
		if call == callback {
			return http.StatusNoContent, ""
		}
		return http.StatusForbidden, missingAccess
	}}
	b := goneBot(t, f)
	if err := b.s.State.GuildAdd(&discordgo.Guild{ID: "g1"}); err != nil {
		t.Fatal(err)
	}
	p := postWithCard(t, b)
	card := b.postCard(p, "g1", "", i18n.Czech)
	if _, err := b.store.UpdatePost(p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	b.syncPost(p.ID)

	b.deleteEnded(time.Now().Add(time.Minute))
	if !f.made("DELETE /channels/c1/messages/m1") {
		t.Errorf("the bot didn't try to delete the card: %v", f.calls)
	}
	if _, ok := b.store.Post(p.ID); ok {
		t.Fatal("the cleanup is held up by a card it can't reach")
	}

	b.onInteraction(nil, click(t, customID(actJoin, p.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card))
	got := answers(f)
	if len(got) != 1 || !strings.Contains(got[0], fmt.Sprintf(`"type":%d`, discordgo.InteractionResponseUpdateMessage)) ||
		strings.Count(got[0], `"disabled":true`) != 2 {
		t.Errorf("answers: %v", got)
	}
}

// Elsewhere the post's buttons only say it is gone: the panels only the
// clicker sees, and the notes in a thread, which aren't its card.
func TestClickElsewhereOnAGonePostOnlyAnswers(t *testing.T) {
	const id = "gadrd3"
	manage := manageButton(id, false, i18n.Czech)
	leave := discordgo.Button{CustomID: customID(actLeave, id), Label: "Leave", Style: discordgo.SecondaryButton}
	for name, c := range map[string]struct {
		action string
		flags  discordgo.MessageFlags
		comps  []discordgo.MessageComponent
	}{
		"/lfg-mine": {actLeave, discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsIsComponentsV2, []discordgo.MessageComponent{
			container(colorFull, discordgo.Section{Components: []discordgo.MessageComponent{text("run")}, Accessory: leave}),
		}},
		"thread note": {actManage, discordgo.MessageFlagsIsComponentsV2, []discordgo.MessageComponent{
			container(colorOpen, discordgo.Section{Components: []discordgo.MessageComponent{text("note")}, Accessory: manage}),
		}},
		"DM": {actManage, discordgo.MessageFlagsIsComponentsV2, []discordgo.MessageComponent{
			container(colorOpen, text("note"), manageRow(id, i18n.Czech)),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDiscord{answer: func(string) (int, string) { return http.StatusNoContent, "" }}
			b := goneBot(t, f)
			b.onInteraction(nil, click(t, customID(c.action, id), c.flags, "en-US", c.comps))
			got := answers(f)
			if len(got) != 1 {
				t.Fatalf("answered %d times: %v", len(got), f.calls)
			}
			if !strings.Contains(got[0], fmt.Sprintf(`"type":%d`, discordgo.InteractionResponseChannelMessageWithSource)) ||
				!strings.Contains(got[0], i18n.English.T("err.not_found")) {
				t.Errorf("answer:\n%s", got[0])
			}
		})
	}
}

// A database that can't be read now doesn't mean the post is gone: its card
// stays as it is, and the clicker can try again.
func TestReadErrorLeavesTheCardAlone(t *testing.T) {
	f := &fakeDiscord{answer: func(string) (int, string) { return http.StatusNoContent, "" }}
	b := goneBot(t, f)
	p := postWithCard(t, b)
	card := b.postCard(p, "g1", "", i18n.Czech)
	b.store.Close()

	b.onInteraction(nil, click(t, customID(actManage, p.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card))

	got := answers(f)
	if len(got) != 1 {
		t.Fatalf("answered %d times: %v", len(got), f.calls)
	}
	if !strings.Contains(got[0], fmt.Sprintf(`"type":%d`, discordgo.InteractionResponseChannelMessageWithSource)) ||
		!strings.Contains(got[0], i18n.English.T("err.generic")) {
		t.Errorf("answer:\n%s", got[0])
	}
}

// If Discord won't take the changed card, the clicker still hears why
// nothing happened, instead of "This interaction failed".
func TestCardDiscordRefusesStillGetsAnAnswer(t *testing.T) {
	f := &fakeDiscord{}
	refused := false
	f.answer = func(string) (int, string) {
		if !refused {
			refused = true
			return http.StatusBadRequest, invalidForm
		}
		return http.StatusNoContent, ""
	}
	b := goneBot(t, f)
	gone, _ := notePost()
	card := b.postCard(gone, "g1", "", i18n.Czech)

	b.onInteraction(nil, click(t, customID(actManage, gone.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card))

	got := answers(f)
	if len(got) != 2 || !strings.Contains(got[1], i18n.English.T("err.not_found")) {
		t.Errorf("answers: %v", got)
	}
}
