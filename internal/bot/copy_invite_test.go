package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

func TestNextStart(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	// The clocks go forward in between; the copy keeps the time of day.
	at := time.Date(2026, 3, 24, 19, 0, 0, 0, prague)
	if got := nextStart(at, prague, at.Add(-time.Hour)); !got.Equal(time.Date(2026, 3, 31, 19, 0, 0, 0, prague)) {
		t.Errorf("a week later: %v", got)
	}
	// It is always in the future.
	if got := nextStart(at, prague, at.AddDate(0, 0, 10)); !got.Equal(time.Date(2026, 4, 7, 19, 0, 0, 0, prague)) {
		t.Errorf("after the next week passed: %v", got)
	}
	if !nextStart(time.Time{}, prague, at).IsZero() {
		t.Error("a run without a start offers one")
	}
}

func TestCopyModal(t *testing.T) {
	b := iconBot(t)
	prague, _ := time.LoadLocation("Europe/Prague")
	p, bob := notePost()
	p.Note = "voice on"
	whoOptions := func(m modal) []string {
		t.Helper()
		for _, c := range m.Components {
			if sel, ok := c.(discordgo.Label).Component.(modalSelect); ok && sel.CustomID == "who" {
				var vals []string
				for _, o := range sel.Options {
					vals = append(vals, o.Value)
				}
				return vals
			}
		}
		return nil
	}

	m := b.copyModal(p, prague, i18n.English)
	start := m.Components[0].(discordgo.Label).Component.(discordgo.TextInput)
	if want := startInput(p.StartAt.AddDate(0, 0, 7), prague); start.CustomID != "start" || start.Value != want || !*start.Required {
		t.Errorf("start field %+v, want %q", start, want)
	}
	if note := m.Components[len(m.Components)-1].(discordgo.Label).Component.(discordgo.TextInput); note.Value != "voice on" {
		t.Errorf("note field %+v", note)
	}
	if m.CustomID != customID(actCopyForm, p.ID) {
		t.Errorf("custom id %q", m.CustomID)
	}
	// Only the party: "everyone" says the same.
	if got := whoOptions(m); !slices.Equal(got, []string{copyAll, copyNone}) {
		t.Errorf("who, with only the party: %v", got)
	}
	p.Applicants = []lfg.Player{bob}
	if got := whoOptions(b.copyModal(p, prague, i18n.English)); !slices.Equal(got, []string{copyAll, copyParty, copyNone}) {
		t.Errorf("who, with a player waiting: %v", got)
	}
	alone := p
	alone.Accepted, alone.Applicants = nil, nil
	if got := whoOptions(b.copyModal(alone, prague, i18n.English)); got != nil {
		t.Errorf("who, for the leader alone: %v", got)
	}

	for _, l := range i18n.Langs {
		for _, post := range []lfg.Post{p, busyPost(b), rally(20, 30)} {
			m := b.copyModal(post, prague, l)
			if len(m.Components) > 5 || len([]rune(m.Title)) > 45 || len(m.CustomID) > 100 {
				t.Errorf("%s copy form %q: %d components", l, m.Title, len(m.Components))
			}
			for _, c := range m.Components {
				if lab := c.(discordgo.Label); len([]rune(lab.Label)) > 45 || len([]rune(lab.Description)) > 100 {
					t.Errorf("%s label %q is too long", l, lab.Label)
				}
			}
			checkSelects(t, m.Components...)
		}
	}
}

func TestManagePanelCopiesAndInvites(t *testing.T) {
	b := iconBot(t)
	long := strings.Repeat("W", 40)
	invites := make([]lfg.Invite, 0, lfg.MaxInvites)
	for n := range lfg.MaxInvites {
		invites = append(invites, lfg.Invite{UserID: fmt.Sprint(900 + n), Name: long, GuildID: "g1"})
	}
	var sent []lfg.Invite
	skipped := map[string][]string{}
	for n := range inviteBatch {
		sent = append(sent, lfg.Invite{Name: long})
		skipped["invite.why_on_post"] = append(skipped["invite.why_on_post"], long+fmt.Sprint(n))
	}

	for _, l := range i18n.Langs {
		done := inviteSummary(sent, skipped, l)
		for name, p := range map[string]lfg.Post{"busy": busyPost(b), "rally": rally(0, 100)} {
			p.Invites = invites
			for _, panel := range []discordgo.MessageComponent{b.managePanel(p, l), b.managePanelSaying(p, done, l)} {
				count, textLen := measure(t, panel)
				if count > maxComponents || textLen > maxTextLength {
					t.Errorf("%s %s manage panel with invitations: %d components, %d characters", l, name, count, textLen)
				}
				checkSelects(t, panel)
				buttons := buttonsOf(panel)
				if _, ok := buttons[customID(actCopy, p.ID)]; !ok {
					t.Errorf("%s %s panel has no copy button", l, name)
				}
				raw := jsonOf(t, panel)
				if !strings.Contains(raw, `"custom_id":"`+customID(actInvite, p.ID)+`"`) ||
					!strings.Contains(raw, `"custom_id":"`+customID(actUninvite, p.ID)+`"`) {
					t.Errorf("%s %s panel has no invite or withdraw menu", l, name)
				}
			}
		}
	}

	// With every invitation open, the picker is off until one is withdrawn.
	p, _ := notePost()
	if picker := findSelect(b.managePanel(p, i18n.English), customID(actInvite, p.ID)); picker == nil ||
		picker.Disabled || picker.MenuType != discordgo.UserSelectMenu || picker.MaxValues != inviteBatch {
		t.Errorf("picker: %+v", picker)
	}
	p.Invites = invites
	if picker := findSelect(b.managePanel(p, i18n.English), customID(actInvite, p.ID)); picker == nil || !picker.Disabled {
		t.Errorf("picker with every invitation open: %+v", picker)
	}

	// An ended post can be copied, but nobody invited.
	p.Status, p.ClosedAt = lfg.StatusClosed, time.Now()
	panel := b.managePanel(p, i18n.English)
	if _, ok := buttonsOf(panel)[customID(actCopy, p.ID)]; !ok {
		t.Error("an ended post can't be copied")
	}
	if strings.Contains(jsonOf(t, panel), customID(actInvite, p.ID)) {
		t.Error("an ended post offers invitations")
	}
}

// findSelect finds a select menu in a component tree.
func findSelect(c discordgo.MessageComponent, customID string) *discordgo.SelectMenu {
	switch v := c.(type) {
	case discordgo.SelectMenu:
		if v.CustomID == customID {
			return &v
		}
	case discordgo.Container:
		for _, cc := range v.Components {
			if s := findSelect(cc, customID); s != nil {
				return s
			}
		}
	case discordgo.ActionsRow:
		for _, cc := range v.Components {
			if s := findSelect(cc, customID); s != nil {
				return s
			}
		}
	}
	return nil
}

func TestCopyAndInviteNotes(t *testing.T) {
	b := iconBot(t)
	p, bob := notePost()
	p.Applicants = []lfg.Player{bob}
	users := make([]string, mentionBatch)
	for n := range users {
		users[n] = fmt.Sprint(100000000000000000 + n)
	}
	for _, l := range i18n.Langs {
		texts, buttons := noteParts(t, b.copiedNote(p, []string{"T"}, "g1", l))
		if !strings.Contains(texts, "<@T>") || !strings.Contains(texts, "<@L>") || !slices.Contains(buttons, customID(actLeave, p.ID)) {
			t.Errorf("%s copy note: %s %v", l, texts, buttons)
		}
		texts, buttons = noteParts(t, b.inviteNote(p, []lfg.Invite{{UserID: "U", Name: "Una", GuildID: "g1"}}, "g1", l))
		if !strings.Contains(texts, "<@U>") || !slices.Contains(buttons, customID(actJoin, p.ID)) {
			t.Errorf("%s invite note: %s %v", l, texts, buttons)
		}
		// Elsewhere the invited player shows by name.
		if texts, _ = noteParts(t, b.inviteNote(p, []lfg.Invite{{UserID: "U", Name: "Una", GuildID: "g1"}}, "g2", l)); strings.Contains(texts, "<@U>") ||
			!strings.Contains(texts, "Una") {
			t.Errorf("%s invite note on another server: %s", l, texts)
		}

		big := busyPost(b)
		for name, c := range map[string]discordgo.MessageComponent{
			"copy":       b.copiedNote(big, users, "g1", l),
			"copy DM":    b.copiedDM(big, big.Applicants[0], l),
			"invite":     b.inviteNote(big, make([]lfg.Invite, inviteBatch), "g1", l),
			"invite DM":  b.inviteDM(big, l),
			"invited in": b.joinNote(big, big.Accepted[0], false, "g1", l),
		} {
			if count, textLen := measure(t, c); count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s: %d components, %d characters", l, name, count, textLen)
			}
		}
	}

	// A player who joined a dungeon party straight away was invited.
	texts, _ := noteParts(t, b.joinNote(p, p.Accepted[0], false, "g1", i18n.English))
	if !strings.Contains(texts, "### ✅ Invitation accepted") {
		t.Errorf("invited player's join note: %s", texts)
	}
	if texts, _ := noteParts(t, b.joinNote(p, bob, false, "g1", i18n.English)); !strings.Contains(texts, "### ✋") {
		t.Errorf("applicant's join note: %s", texts)
	}
}

func TestMinePanelListsInvitations(t *testing.T) {
	b := iconBot(t)
	b.commandIDs = map[string]string{}
	p, _ := notePost()
	p.Invites = []lfg.Invite{{UserID: "me", Name: "Me", GuildID: "g1"}}
	panel := b.minePanel([]lfg.Post{p}, "me", "g1", i18n.English)
	texts, buttons := noteParts(t, panel)
	if !strings.Contains(texts, "Invited (1)") || !slices.Contains(buttons, customID(actJoin, p.ID)) ||
		slices.Contains(buttons, customID(actLeave, p.ID)) {
		t.Errorf("mine panel: %s %v", texts, buttons)
	}
}

// inviteDiscord answers like Discord: messages get IDs, threads start, and
// everyone is a member of the server they're asked about.
func inviteDiscord() *fakeDiscord {
	sent := 0
	return &fakeDiscord{answer: func(call string) (int, string) {
		method, path, _ := strings.Cut(call, " ")
		switch {
		case call == callback:
			return http.StatusNoContent, ""
		case method == "POST" && path == "/users/@me/channels":
			return http.StatusOK, `{"id":"dm","type":1}`
		case method == "POST" && strings.HasSuffix(path, "/messages"):
			sent++
			return http.StatusOK, fmt.Sprintf(`{"id":"%d"}`, 500+sent)
		}
		return http.StatusOK, "{}"
	}}
}

// interaction is an interaction from Discord, by a user on server g1.
func interaction(t *testing.T, typ discordgo.InteractionType, userID, data string) *discordgo.InteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"9","application_id":"8","type":%d,"token":"tok","guild_id":"g1","channel_id":"c1","locale":"cs",
		"member":{"user":{"id":%q,"username":%q},"permissions":"0"},
		"message":{"id":"panel","channel_id":"c1","flags":32768,"components":[]},
		"data":%s}`, typ, userID, userID, data)
	var ic discordgo.InteractionCreate
	if err := json.Unmarshal([]byte(raw), &ic); err != nil {
		t.Fatal(err)
	}
	return &ic
}

// sentTo lists the bodies of the messages the bot posted in a channel.
func sentTo(f *fakeDiscord, channelID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, c := range f.calls {
		if c == "POST /channels/"+channelID+"/messages" {
			out = append(out, f.bodies[n])
		}
	}
	return out
}

func lastAnswer(f *fakeDiscord) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for n := len(f.calls) - 1; n >= 0; n-- {
		if f.calls[n] == callback {
			return f.bodies[n]
		}
	}
	return ""
}

func TestInviteFlow(t *testing.T) {
	t.Parallel()
	f := inviteDiscord()
	b := goneBot(t, f)
	p := postWithCard(t, b)
	p, _ = b.store.UpdatePost(p.ID, func(p *lfg.Post) error { p.MinGS = 3000; return nil })

	// The leader picks a player, a bot and someone already on the post.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "L", fmt.Sprintf(`{"custom_id":%q,"component_type":5,
		"values":["U","robot","T"],
		"resolved":{"users":{"U":{"id":"U","username":"una","global_name":"Una"},"robot":{"id":"robot","username":"robot","bot":true},
			"T":{"id":"T","username":"tom"}},"members":{"U":{"nick":"Unicorn"}}}}`, customID(actInvite, p.ID))))
	b.syncWG.Wait()
	p, _ = b.store.Post(p.ID)
	if len(p.Invites) != 1 || p.Invites[0] != (lfg.Invite{UserID: "U", Name: "Unicorn", GuildID: "g1"}) {
		t.Fatalf("invitations: %+v", p.Invites)
	}
	if answer := lastAnswer(f); !strings.Contains(answer, "Unicorn") || !strings.Contains(answer, "robot") || !strings.Contains(answer, "tom") {
		t.Errorf("the panel doesn't say whom it invited and whom not:\n%s", answer)
	}
	notes := sentTo(f, "m1")
	if len(notes) != 1 || !strings.Contains(notes[0], `"users":["U"]`) || !strings.Contains(notes[0], customID(actJoin, p.ID)) {
		t.Fatalf("the invitation in the thread: %q", notes)
	}

	// The invited player can join as anything, below the gear score.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "U",
		fmt.Sprintf(`{"custom_id":%q,"component_type":2}`, customID(actJoin, p.ID))))
	if form := lastAnswer(f); !strings.Contains(form, `"value":"cleric|healer"`) || !strings.Contains(form, `"required":false`) {
		t.Errorf("join form for the invited player:\n%s", form)
	}
	b.onInteraction(nil, interaction(t, discordgo.InteractionModalSubmit, "U", fmt.Sprintf(`{"custom_id":%q,"components":[
		{"type":18,"component":{"type":3,"custom_id":"choice","values":["cleric|healer"]}},
		{"type":18,"component":{"type":4,"custom_id":"gs","value":"1200"}},
		{"type":18,"component":{"type":4,"custom_id":"char","value":"Uni"}}]}`, customID(actJoinForm, p.ID))))
	b.syncWG.Wait()
	p, _ = b.store.Post(p.ID)
	if !p.InParty("U") || p.Missing != 1 || len(p.Invites) != 0 || !slices.Contains(p.Pinged, "U") || !slices.Contains(p.Pinged, "L") {
		t.Fatalf("after the invited player joined: %+v", p)
	}
	notes = sentTo(f, "m1")
	if len(notes) != 2 || !strings.Contains(notes[1], `"users":["L"]`) || !strings.Contains(notes[1], "Pozvánka přijata") {
		t.Errorf("the leader's note: %q", notes)
	}

	// Anyone else still needs the gear score and waits for the leader.
	b.onInteraction(nil, interaction(t, discordgo.InteractionModalSubmit, "V", fmt.Sprintf(`{"custom_id":%q,"components":[
		{"type":18,"component":{"type":3,"custom_id":"choice","values":["gladiator|tank"]}},
		{"type":18,"component":{"type":4,"custom_id":"gs","value":"3100"}}]}`, customID(actJoinForm, p.ID))))
	if p, _ = b.store.Post(p.ID); slices.IndexFunc(p.Applicants, func(pl lfg.Player) bool { return pl.UserID == "V" }) < 0 {
		t.Errorf("an uninvited player isn't waiting: %+v", p)
	}

	// Withdrawing an invitation.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "L", fmt.Sprintf(`{"custom_id":%q,"component_type":5,
		"values":["W"],"resolved":{"users":{"W":{"id":"W","username":"wes"}}}}`, customID(actInvite, p.ID))))
	if p, _ = b.store.Post(p.ID); !p.IsInvited("W") {
		t.Fatalf("not invited: %+v", p.Invites)
	}
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "L", fmt.Sprintf(`{"custom_id":%q,"component_type":3,
		"values":["W"]}`, customID(actUninvite, p.ID))))
	if p, _ = b.store.Post(p.ID); len(p.Invites) != 0 {
		t.Errorf("not withdrawn: %+v", p.Invites)
	}

	// Only the leader invites.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "T", fmt.Sprintf(`{"custom_id":%q,"component_type":5,
		"values":["X"],"resolved":{"users":{"X":{"id":"X","username":"x"}}}}`, customID(actInvite, p.ID))))
	if p, _ = b.store.Post(p.ID); len(p.Invites) != 0 {
		t.Errorf("a party member invited someone: %+v", p.Invites)
	}
	b.syncWG.Wait()
}

func TestCopyFlow(t *testing.T) {
	t.Parallel()
	f := inviteDiscord()
	b := goneBot(t, f)
	src := postWithCard(t, b)
	// Bob joined from a server without the post, so he hears by DM; Tom is
	// in the party on this one.
	_, bob := notePost()
	src, _ = b.store.UpdatePost(src.ID, func(p *lfg.Post) error {
		p.Note = "old note"
		return p.AddApplicant(bob)
	})

	prague, _ := time.LoadLocation("Europe/Prague")
	start := time.Now().Add(7 * 24 * time.Hour).Truncate(time.Minute)
	submit := func(userID, who, at string) {
		t.Helper()
		b.onInteraction(nil, interaction(t, discordgo.InteractionModalSubmit, userID, fmt.Sprintf(`{"custom_id":%q,"components":[
			{"type":18,"component":{"type":4,"custom_id":"start","value":%q}},
			{"type":18,"component":{"type":3,"custom_id":"who","values":[%q]}},
			{"type":18,"component":{"type":4,"custom_id":"note","value":"next week"}}]}`, customID(actCopyForm, src.ID), at, who)))
		b.syncWG.Wait()
	}

	// Only the leader copies, and only to a start to come.
	submit("T", copyAll, startInput(start, prague))
	submit("L", copyAll, "01.01.2020 10:00")
	submit("L", copyAll, "whenever")
	if runs := b.store.ActivePostsByLeader("L"); len(runs) != 1 {
		t.Fatalf("copied when it shouldn't: %+v", runs)
	}

	submit("L", copyAll, startInput(start, prague))
	runs := b.store.ActivePostsByLeader("L")
	if len(runs) != 2 {
		t.Fatalf("not copied: %v", f.calls)
	}
	c := runs[1]
	switch {
	case c.ID == src.ID || !c.StartAt.Equal(start) || c.Note != "next week" || c.DungeonID != src.DungeonID || c.ModeID != src.ModeID:
		t.Errorf("the copy: %+v", c)
	case !c.InParty("T") || len(c.Applicants) != 1 || c.Applicants[0].UserID != "B" || c.Missing != src.Missing:
		t.Errorf("not everyone came along: %+v", c)
	case !slices.Contains(c.Pinged, "T") || slices.Contains(c.Pinged, "B") || slices.Contains(c.Pinged, "L"):
		t.Errorf("pinged: %v", c.Pinged)
	case len(c.Messages) != 1 || !c.Messages[0].Thread:
		t.Errorf("the copy's card: %+v", c.Messages)
	}
	if old, _ := b.store.Post(src.ID); old.Note != "old note" || !old.Active() || len(old.People()) != 3 {
		t.Errorf("the old post changed: %+v", old)
	}
	notes := sentTo(f, c.Messages[0].MessageID)
	if len(notes) != 1 || !strings.Contains(notes[0], `"users":["T"]`) || !strings.Contains(notes[0], customID(actLeave, c.ID)) {
		t.Errorf("the note in the copy's thread: %q", notes)
	}
	if dms := sentTo(f, "dm"); len(dms) != 1 || !strings.Contains(dms[0], customID(actLeave, c.ID)) {
		t.Errorf("Bob's DM: %q", dms)
	}
	if !f.made("PATCH /webhooks/8/tok/messages/@original") {
		t.Errorf("the leader wasn't told: %v", f.calls)
	}

	// A copy with nobody along tells nobody.
	before := len(sentTo(f, "dm"))
	submit("L", copyNone, startInput(start.Add(time.Hour), prague))
	runs = b.store.ActivePostsByLeader("L")
	if len(runs) != 3 || len(runs[2].People()) != 1 || runs[2].Missing != src.Missing+1 {
		t.Fatalf("copy with nobody: %+v", runs)
	}
	if len(sentTo(f, runs[2].Messages[0].MessageID)) != 0 || len(sentTo(f, "dm")) != before {
		t.Error("a copy with nobody on it told someone")
	}
}
