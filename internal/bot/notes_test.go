package bot

import (
	"encoding/json"
	"fmt"
	"slices"
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

		// A player who left is linked where they're a member, with how the
		// party stands now; the note has no buttons.
		for _, c := range []struct {
			r   lfg.Removal
			key string
		}{
			{lfg.Removal{Player: bob, WasAccepted: true}, "thread.left"},
			{lfg.Removal{Player: bob, WasReserve: true}, "thread.left_reserve"},
			{lfg.Removal{Player: bob}, "thread.left_applicant"},
		} {
			texts, buttons := noteParts(t, b.leaveNote(p, c.r, "g2", l))
			for _, want := range []string{"### 👋", l.T(c.key, b.runName(p)), "<@B>", "`Bobby`", b.statusLine(p, l)} {
				if !strings.Contains(texts, want) {
					t.Errorf("%s leave note (%s) misses %q:\n%s", l, c.key, want, texts)
				}
			}
			if len(buttons) != 0 {
				t.Errorf("%s leave note has buttons %v", l, buttons)
			}
		}
		rally := p
		rally.Rally = true
		texts, _ = noteParts(t, b.leaveNote(rally, lfg.Removal{Player: bob, WasAccepted: true}, "g1", l))
		if !strings.Contains(texts, l.T("thread.abyss_left", b.runName(rally))) || !strings.Contains(texts, "**Bob**") {
			t.Errorf("%s leave note of a rally:\n%s", l, texts)
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
			"left":     b.leaveNote(p, lfg.Removal{Player: pl, WasAccepted: true}, "g1", l),
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

// buttonsOf maps the custom IDs of a panel's buttons to the buttons.
func buttonsOf(c discordgo.MessageComponent) map[string]discordgo.Button {
	out := map[string]discordgo.Button{}
	var walk func(c discordgo.MessageComponent)
	walk = func(c discordgo.MessageComponent) {
		switch v := c.(type) {
		case discordgo.Container:
			for _, cc := range v.Components {
				walk(cc)
			}
		case discordgo.ActionsRow:
			for _, cc := range v.Components {
				walk(cc)
			}
		case discordgo.Section:
			walk(v.Accessory)
		case discordgo.Button:
			out[v.CustomID] = v
		}
	}
	walk(c)
	return out
}

func TestManageButtonAndReopen(t *testing.T) {
	b := testBot(t)
	p, _ := notePost()
	// Grey where everyone sees it, green where only the leader does.
	if m := buttonsOf(b.joinNote(p, p.Accepted[0], false, "g1", i18n.English))[customID(actManage, p.ID)]; m.Style != discordgo.SecondaryButton {
		t.Errorf("join note's Manage button %+v", m)
	}
	card := b.postCard(p, "g1", "", i18n.English)
	if m := buttonsOf(card[0])[customID(actManage, p.ID)]; m.Style != discordgo.SecondaryButton || m.Disabled {
		t.Errorf("card's Manage button %+v", m)
	}
	if m := manageRow(p.ID, i18n.English).Components[0].(discordgo.Button); m.Style != discordgo.SuccessButton {
		t.Errorf("leader's own Manage button %+v", m)
	}
	if _, ok := buttonsOf(b.managePanel(p, i18n.English))[customID(actMove, p.ID)]; !ok {
		t.Error("the manage panel of an open post has no dungeon change")
	}

	// An ended post keeps Manage on its card, and its panel offers Reopen.
	p.Close(lfg.StatusClosed, time.Now())
	card = b.postCard(p, "g1", "", i18n.English)
	buttons := buttonsOf(card[0])
	if m := buttons[customID(actManage, p.ID)]; m.Disabled || !buttons[customID(actJoin, p.ID)].Disabled {
		t.Errorf("ended card buttons %+v", buttons)
	}
	if r, ok := buttonsOf(b.managePanel(p, i18n.English))[customID(actReopen, p.ID)]; !ok || r.Style != discordgo.SuccessButton {
		t.Errorf("ended panel has no green Reopen: %+v", r)
	}
}

func TestMovePanel(t *testing.T) {
	b := testBot(t)
	p, _ := notePost() // Krao Cave, a party of 5: leader, Tom, 2 missing, so 1 found outside
	p.Missing = 2
	yes := func(dungeonID, modeID string) (discordgo.Button, string) {
		panel := b.movePanel(p, dungeonID, modeID, i18n.English)
		texts, _ := noteParts(t, panel)
		for id, btn := range buttonsOf(panel) {
			if strings.HasPrefix(id, "lfg:"+actMoveYes+":") {
				return btn, texts
			}
		}
		t.Fatalf("no confirm button for %s/%s", dungeonID, modeID)
		return discordgo.Button{}, ""
	}

	if btn, _ := yes(p.DungeonID, p.ModeID); !btn.Disabled {
		t.Error("moving to the same dungeon and mode can be confirmed")
	}
	// Dredgion Depot takes 4: leader, Tom and the one found outside fit.
	btn, texts := yes("dredgion-depot", "")
	action, rest, _ := parseCustomID(btn.CustomID)
	postID, pick, _ := strings.Cut(rest, ":")
	dungeonID, modeID, _ := strings.Cut(pick, ":")
	if btn.Disabled || action != actMoveYes || postID != p.ID || dungeonID != "dredgion-depot" || modeID != "easy" {
		t.Errorf("confirm %+v → %s %s %s %s", btn, action, postID, dungeonID, modeID)
	}
	if !strings.Contains(texts, "Dredgion Depot") || !strings.Contains(texts, "(3/4)") {
		t.Errorf("move preview:\n%s", texts)
	}
	// A full party of 5, three of them found outside, doesn't fit into 4.
	p.Missing = 0
	if btn, texts := yes("dredgion-depot", "hard"); !btn.Disabled || !strings.Contains(texts, "⚠️") {
		t.Errorf("a party too big can be moved: %+v\n%s", btn, texts)
	}
	for _, l := range i18n.Langs {
		count, textLen := measure(t, b.movePanel(p, "abyssal-forge-ludra", "hard", l))
		if count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s move panel: %d components, %d characters", l, count, textLen)
		}
	}
}

func TestLeavingIsToldInTheThreadWithoutAPing(t *testing.T) {
	t.Parallel()
	f := inviteDiscord()
	b := goneBot(t, f)
	p := postWithCard(t, b)
	b.store.SetThread(p.ID, "m1")
	// The party is full, and Rex waits as a tank reserve.
	if _, err := b.store.UpdatePost(p.ID, func(p *lfg.Post) error {
		p.Missing, p.Status = 0, lfg.StatusFull
		p.Reserves = []lfg.Player{{UserID: "R", Name: "Rex", ClassID: "templar", RoleID: "tank", GuildID: "g1"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Tom leaves: the thread hears it, pinging nobody, and then welcomes
	// Rex, who took his slot, with a ping.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "T",
		fmt.Sprintf(`{"custom_id":%q,"component_type":2}`, customID(actLeave, p.ID))))
	b.syncWG.Wait()
	cs := i18n.Czech
	if got := lastAnswer(f); !strings.Contains(got, "Už nejsi v náboru") {
		t.Errorf("the answer to Tom: %s", got)
	}
	notes := sentTo(f, "m1")
	if len(notes) != 2 {
		t.Fatalf("the notes in the thread: %q", notes)
	}
	var body any
	if err := json.Unmarshal([]byte(notes[0]), &body); err != nil {
		t.Fatal(err)
	}
	if left := jsonOf(t, body); len(pinged(t, notes[0])) != 0 || !strings.Contains(left, cs.T("thread.left_title")) ||
		!strings.Contains(left, "<@T>") || !strings.Contains(left, "Parta je plná") {
		t.Errorf("the note that Tom left: %s", left)
	}
	if !slices.Equal(pinged(t, notes[1]), []string{"R"}) || !strings.Contains(notes[1], "Posunul ses") {
		t.Errorf("Rex's welcome: %s", notes[1])
	}

	// A player who isn't on the post can't leave it, and the thread hears
	// nothing.
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "T",
		fmt.Sprintf(`{"custom_id":%q,"component_type":2}`, customID(actLeave, p.ID))))
	b.syncWG.Wait()
	if notes := sentTo(f, "m1"); len(notes) != 2 {
		t.Errorf("a second leave was told: %q", notes)
	}
}
