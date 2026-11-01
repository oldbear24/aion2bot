package bot

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

func TestManagePanelChangesLookingForAndHandsOver(t *testing.T) {
	b := testBot(t)
	p, _ := notePost()
	for name, post := range map[string]lfg.Post{"dungeon": p, "rally": rally(10, 3)} {
		ids := selectIDs(t, b.managePanel(post, i18n.English))
		if !slices.Contains(ids, customID(actLooking, post.ID)) || !slices.Contains(ids, customID(actHandTo, post.ID)) {
			t.Errorf("%s manage panel selects %v", name, ids)
		}
	}
	// The Looking for menu starts at what the post looks for.
	raw := jsonOf(t, b.managePanel(p, i18n.English))
	for _, want := range []string{`"default":true,"description":"Gladiator, Templar","emoji":{"name":"🟦"},"label":"Tank"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("manage panel lacks %s", want)
		}
	}
	// Nobody to hand over to without a party; nothing to change once ended.
	alone := p
	alone.Accepted = nil
	if slices.Contains(selectIDs(t, b.managePanel(alone, i18n.English)), customID(actHandTo, p.ID)) {
		t.Error("hand over offered without a party")
	}
	ended := p
	ended.Close(lfg.StatusClosed, time.Now())
	if ids := selectIDs(t, b.managePanel(ended, i18n.English)); len(ids) != 0 {
		t.Errorf("an ended post's panel has menus %v", ids)
	}

	// The confirm buttons carry the new leader and whether the old one
	// stays.
	for _, byLeader := range []bool{true, false} {
		panel := b.handOverPanel(p, "T", byLeader, i18n.English)
		texts, buttons := noteParts(t, panel)
		if !slices.Contains(buttons, customID(actHandYes, p.ID+":T:stay")) || !slices.Contains(buttons, customID(actHandYes, p.ID+":T:leave")) ||
			!slices.Contains(buttons, customID(actPanel, p.ID)) || !strings.Contains(texts, "**Tom**") {
			t.Errorf("hand over panel (by leader %v): %v\n%s", byLeader, buttons, texts)
		}
		if strings.Contains(texts, "**Lea**") == byLeader {
			t.Errorf("hand over panel (by leader %v) names the leader wrongly:\n%s", byLeader, texts)
		}
		action, rest, _ := parseCustomID(customID(actHandYes, p.ID+":T:stay"))
		postID, pick, _ := strings.Cut(rest, ":")
		if userID, how, _ := strings.Cut(pick, ":"); action != actHandYes || postID != p.ID || userID != "T" || how != "stay" {
			t.Errorf("hand over IDs: %s %s %s %s", action, postID, userID, how)
		}
	}
	if _, buttons := noteParts(t, b.handOverPanel(p, "nobody", true, i18n.English)); len(buttons) != 0 {
		t.Error("hand over to someone not in the party can be confirmed")
	}
}

func TestHandOverAndLookingForNotes(t *testing.T) {
	b := testBot(t)
	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	b.s = s
	before, _ := notePost()
	after := before
	after.Accepted = slices.Clone(before.Accepted)
	if _, err := after.HandOver("T", false); err != nil {
		t.Fatal(err)
	}
	for _, l := range i18n.Langs {
		texts, buttons := noteParts(t, b.handOverNote(before, after, false, "", "g1", l))
		if !strings.Contains(texts, "<@T>") || !strings.Contains(texts, "<@L>") || strings.Contains(texts, "g9") ||
			len(buttons) != 1 || buttons[0] != customID(actManage, after.ID) {
			t.Errorf("%s hand over note: %v\n%s", l, buttons, texts)
		}
		// Moderators who handed it over are named.
		if texts, _ := noteParts(t, b.handOverNote(before, after, true, "g9", "g2", l)); !strings.Contains(texts, "*g9*") ||
			!strings.Contains(texts, "**Tom**") {
			t.Errorf("%s hand over note by moderators:\n%s", l, texts)
		}

		changed := before
		changed.LookingFor = []string{"healer"}
		texts, _ = noteParts(t, b.changesNote(before, changed, "g1", l))
		if !strings.Contains(texts, "🔎") || !strings.Contains(texts, "Healer") || strings.Contains(texts, "Tank") {
			t.Errorf("%s changes note for Looking for:\n%s", l, texts)
		}
	}

	big := busyPost(iconBot(t))
	for _, l := range i18n.Langs {
		note := b.handOverNote(big, big, true, "g9", "g1", l)
		if count, textLen := measure(t, note); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s hand over note: %d components, %d characters", l, count, textLen)
		}
		if texts, _ := noteParts(t, note); strings.Contains(texts, "%!") || strings.Contains(texts, "thread.") {
			t.Errorf("%s hand over note has a broken text:\n%s", l, texts)
		}
	}
}

func TestMinePanel(t *testing.T) {
	b := iconBot(t)
	lead, _ := notePost()
	lead.Messages = []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}}
	joined := rally(10, 0)
	joined.ID, joined.LeaderID, joined.LeaderName = "r1", "X", "Xena"
	joined.AddApplicant(lfg.Player{UserID: "L", Name: "Lea", ClassID: "cleric", RoleID: "healer"})
	ended := lead
	ended.ID = "e1"
	ended.Close(lfg.StatusClosed, time.Now())

	panel := b.minePanel([]lfg.Post{ended, joined, lead}, "L", "g1", i18n.English)
	raw := jsonOf(t, panel)
	_, buttons := noteParts(t, panel)
	if !slices.Contains(buttons, customID(actManage, lead.ID)) || !slices.Contains(buttons, customID(actLeave, joined.ID)) ||
		!slices.Contains(buttons, customID(actManage, ended.ID)) {
		t.Errorf("mine buttons %v", buttons)
	}
	for _, want := range []string{"Leading (1)", "Joined (1)", "Ended", "You're going", "https://discord.com/channels/g1/c1/m1"} {
		if !strings.Contains(raw, want) {
			t.Errorf("mine panel lacks %q:\n%s", want, raw)
		}
	}
	if none := jsonOf(t, b.minePanel(nil, "L", "g1", i18n.Czech)); !strings.Contains(none, "nejsi v žádném") {
		t.Errorf("empty mine panel:\n%s", none)
	}

	// A busy player's list fits, and counts the runs it leaves out.
	var many []lfg.Post
	for n := range 12 {
		p := busyPost(b)
		p.ID = fmt.Sprint("p", n)
		p.Messages = lead.Messages
		if n%2 == 0 {
			p.LeaderID = "me"
		} else {
			p.Reserves = append(p.Reserves, lfg.Player{UserID: "me", Name: "Me", ClassID: "ranger", RoleID: "dps"})
		}
		many = append(many, p)
	}
	for _, l := range i18n.Langs {
		panel := b.minePanel(many, "me", "g1", l)
		if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s mine panel: %d components, %d characters", l, count, textLen)
		}
		if texts, _ := noteParts(t, panel); !strings.Contains(texts, l.T("roster.more", 4)) || strings.Contains(texts, "%!") {
			t.Errorf("%s mine panel doesn't count the rest:\n%s", l, texts)
		}
	}
}
