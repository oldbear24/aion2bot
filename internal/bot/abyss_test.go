package bot

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// rally is an open Abyss rally of the given size with players in it.
func rally(size, players int) lfg.Post {
	p := lfg.Post{ID: "abc123", LeaderID: "L", LeaderName: "Lea", GuildID: "g1", DungeonID: "artifact-siege",
		ModeID: "lower-reshanta", LookingFor: []string{config.AnyID}, Rally: true, Size: size, Status: lfg.StatusOpen,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	for n := range players {
		p.AddApplicant(lfg.Player{UserID: fmt.Sprint(100 + n), Name: fmt.Sprint("P", n), ClassID: "ranger", RoleID: "dps", GuildID: "g1"})
	}
	return p
}

// selectIDs lists the custom IDs of a panel's select menus.
func selectIDs(t *testing.T, c discordgo.MessageComponent) []string {
	t.Helper()
	var ids []string
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
		case discordgo.SelectMenu:
			ids = append(ids, v.CustomID)
		}
	}
	walk(c)
	return ids
}

func TestAbyssDraft(t *testing.T) {
	b := testBot(t)
	d := draft{Abyss: true, LookingFor: []string{config.AnyID}}
	b.selectDungeon(&d, "krao-cave")
	if d.DungeonID != "" {
		t.Fatal("the /lfg-abyss panel took a dungeon")
	}
	b.selectDungeon(&d, "artifact-siege")
	if d.DungeonID != "artifact-siege" || d.ModeID != "lower-reshanta" {
		t.Fatalf("draft %+v", d)
	}
	dungeon := draft{LookingFor: []string{config.AnyID}, Missing: 1}
	if b.selectDungeon(&dungeon, "artifact-siege"); dungeon.DungeonID != "" {
		t.Fatal("the /lfg panel took an Abyss event")
	}

	d.Faction = "elyos"
	full := d
	full.Size, full.Note, full.Problem = 100, strings.Repeat("n", 300), "draft.abyss_need_dungeon"
	full.StartAt, full.Server = time.Now().Add(time.Hour), strings.Repeat("S", maxChar)
	for _, l := range i18n.Langs {
		for name, d := range map[string]draft{"empty": {Abyss: true, LookingFor: []string{config.AnyID}}, "full": full} {
			panel := b.draftPanel(d, l)
			if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s abyss panel: %d components, %d characters", l, name, count, textLen)
			}
			checkSelects(t, panel)
			raw := jsonOf(t, panel)
			if strings.Contains(raw, draftPrefix) || strings.Contains(raw, ".abyss_") {
				t.Errorf("%s %s abyss panel uses the /lfg panel's IDs or lacks a text:\n%s", l, name, raw)
			}
		}
	}

	panel := b.draftPanel(d, i18n.English)
	ids := selectIDs(t, panel)
	if !slices.Contains(ids, draftID(fieldSize, true)) || slices.Contains(ids, draftID(fieldMissing, true)) {
		t.Errorf("abyss panel selects %v", ids)
	}
	raw := jsonOf(t, panel)
	for _, want := range []string{"Artifact Siege", "Dimensional Invasion", "Lower Reshanta", "No limit", "right away"} {
		if !strings.Contains(raw, want) {
			t.Errorf("abyss panel lacks %q", want)
		}
	}
	if strings.Contains(raw, "Krao Cave") || postDisabled(panel) {
		t.Errorf("abyss panel lists dungeons or can't post:\n%s", raw)
	}
}

func TestRallyCard(t *testing.T) {
	b := testBot(t)
	text := func(p lfg.Post, l i18n.Lang) string { return jsonOf(t, b.postCard(p, "g1", "", l)) }

	p := rally(0, 2)
	en := text(p, i18n.English)
	for _, want := range []string{"Artifact Siege", "Lower Reshanta", "👥 3 going", "Any class", "Going (3)", "right away"} {
		if !strings.Contains(en, want) {
			t.Errorf("rally card lacks %q:\n%s", want, en)
		}
	}
	if strings.Contains(en, "(3/") || strings.Contains(en, "Waiting") {
		t.Errorf("an unlimited rally shows a size or players waiting:\n%s", en)
	}
	if cs := text(p, i18n.Czech); !strings.Contains(cs, "Přihlášeno: 3") || !strings.Contains(cs, "Přihlášení (3)") {
		t.Errorf("Czech rally card:\n%s", cs)
	}

	p = rally(5, 6) // the leader and four players fill it, two wait
	full := text(p, i18n.English)
	for _, want := range []string{"Group full", "(5/5)", "Join as reserve", "Reserves (2)"} {
		if !strings.Contains(full, want) {
			t.Errorf("full rally card lacks %q:\n%s", want, full)
		}
	}

	big := rally(0, 120)
	for _, l := range i18n.Langs {
		comps := iconBot(t).postCard(big, "g1", "123", l)
		if count, textLen := measure(t, comps...); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s card of a rally of 121: %d components, %d characters", l, count, textLen)
		}
	}
}

func TestRallyManagePanel(t *testing.T) {
	b := testBot(t)
	p := rally(10, 3)
	panel := b.managePanel(p, i18n.English)
	ids := selectIDs(t, panel)
	if !slices.Contains(ids, customID(actSize, p.ID)) || !slices.Contains(ids, customID(actKick, p.ID)) || slices.Contains(ids, customID(actAccept, p.ID)) {
		t.Errorf("rally manage selects %v", ids)
	}
	buttons := buttonsOf(panel)
	for _, id := range []string{actEdit, actMove, actClose, actDelete} {
		if _, ok := buttons[customID(id, p.ID)]; !ok {
			t.Errorf("rally manage lacks %s", id)
		}
	}
	if _, ok := buttons[customID(actMore, p.ID)]; ok {
		t.Error("a rally has no missing count to change")
	}
	raw := jsonOf(t, panel)
	for _, want := range []string{"Change event", "Remove players", "(4/10)"} {
		if !strings.Contains(raw, want) {
			t.Errorf("rally manage lacks %q:\n%s", want, raw)
		}
	}
	// Sizes below the four already in it aren't offered.
	if opts := sizeOptions(10, 4, i18n.English); opts[0].Value != "0" || opts[1].Value != "5" || !opts[2].Default {
		t.Errorf("size options %+v", opts)
	}
	if opts := sizeOptions(0, 31, i18n.English); opts[1].Value != "40" || !opts[0].Default {
		t.Errorf("size options for 31 players %+v", opts)
	}

	// The remove menu offers the latest 25 of a big rally.
	big := rally(0, 60)
	for _, l := range i18n.Langs {
		panel := iconBot(t).managePanel(big, l)
		if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s manage panel of a rally of 61: %d components, %d characters", l, count, textLen)
		}
		checkSelects(t, panel)
	}
	if raw := jsonOf(t, b.managePanel(big, i18n.English)); !strings.Contains(raw, `"value":"135"`) || strings.Contains(raw, `"value":"134"`) {
		t.Error("the remove menu doesn't offer the latest players")
	}
}

func TestRallyMovesAmongAbyssEvents(t *testing.T) {
	b := testBot(t)
	p := rally(10, 3)
	panel := b.movePanel(p, "abyss-boss", "", i18n.English)
	raw := jsonOf(t, panel)
	if !strings.Contains(raw, "Change the event") || strings.Contains(raw, "Krao Cave") || !strings.Contains(raw, "(4/10)") {
		t.Errorf("rally move panel:\n%s", raw)
	}
	confirm := func(panel discordgo.MessageComponent) discordgo.Button {
		for id, btn := range buttonsOf(panel) {
			if strings.HasPrefix(id, "lfg:"+actMoveYes+":") {
				return btn
			}
		}
		t.Fatal("no confirm button")
		return discordgo.Button{}
	}
	if btn := confirm(panel); btn.Disabled {
		t.Error("a rally can't move to the Abyss boss")
	}
	if btn := confirm(b.movePanel(p, "krao-cave", "conquest", i18n.English)); !btn.Disabled {
		t.Error("a rally can move to a dungeon")
	}
	dungeon, _ := notePost()
	if btn := confirm(b.movePanel(dungeon, "abyss-boss", "", i18n.English)); !btn.Disabled {
		t.Error("a dungeon post can move to an Abyss event")
	}
	moved := p
	moved.DungeonID = "abyss-boss"
	if note, _ := noteParts(t, b.moveNote(p, moved, "g1", i18n.English)); !strings.Contains(note, "Event changed") {
		t.Errorf("move note:\n%s", note)
	}
}

func TestRallyNotes(t *testing.T) {
	b := testBot(t)
	p := rally(0, 1)
	p.StartAt = time.Now().Add(15 * time.Minute)
	join, _ := noteParts(t, b.joinNote(p, p.Accepted[0], false, "g1", i18n.English))
	if !strings.Contains(join, "first player joined your **Artifact Siege** (Lower Reshanta) rally") || strings.Contains(join, "Accept") {
		t.Errorf("rally join note:\n%s", join)
	}

	// The reminder mentions every user it pings, as the roster may not
	// name them all.
	users := make([]string, mentionBatch)
	for n := range users {
		users[n] = fmt.Sprint(900000000000000000 + n)
	}
	for _, l := range i18n.Langs {
		note := b.rallyReminderNote(rally(0, 80), users, l)
		texts, _ := noteParts(t, note)
		for _, id := range users {
			if !strings.Contains(texts, "<@"+id+">") {
				t.Fatalf("%s reminder doesn't mention %s", l, id)
			}
		}
		if count, textLen := measure(t, note); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s rally reminder: %d components, %d characters", l, count, textLen)
		}
	}
}

func TestAbyssEverywhere(t *testing.T) {
	b := iconBot(t)
	commands := commandList(b.game)
	names := map[string]*discordgo.ApplicationCommand{}
	for _, c := range commands {
		names[c.Name] = c
	}
	if names["lfg-abyss"] == nil {
		t.Fatal("no /lfg-abyss")
	}
	if !slices.ContainsFunc(names["lfg-find"].Options[0].Choices, func(c *discordgo.ApplicationCommandOptionChoice) bool {
		return c.Value == "artifact-siege"
	}) {
		t.Error("/lfg-find can't filter by Abyss event")
	}
	without := *b.game
	without.Abyss = nil
	if slices.ContainsFunc(commandList(&without), func(c *discordgo.ApplicationCommand) bool { return c.Name == "lfg-abyss" }) {
		t.Error("/lfg-abyss without Abyss events")
	}

	p := rally(0, 2)
	p.Messages = []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}}
	if find := jsonOf(t, b.findPanel([]lfg.Post{p}, "g1", lfg.Profile{}, i18n.English)); !strings.Contains(find, "3 going") ||
		!strings.Contains(find, "right away") {
		t.Errorf("find panel with a rally:\n%s", find)
	}
	if dm := jsonOf(t, b.alertDM(p, p.Messages[0], i18n.English)); !strings.Contains(dm, "3 going") {
		t.Errorf("alert DM for a rally:\n%s", dm)
	}
	if alerts := jsonOf(t, b.alertPanel(lfg.Alert{Dungeons: []string{"rift-invasion"}, Choices: []string{"ranger|dps"}}, lfg.Profile{}, i18n.English)); !strings.Contains(alerts, "Alerts are on** for Rift Invasion") {
		t.Errorf("alerts for an Abyss event:\n%s", alerts)
	}
	for _, l := range i18n.Langs {
		help := jsonOf(t, b.helpPanel(l, true))
		if !strings.Contains(help, "/lfg-abyss") || !strings.Contains(help, "Artifact Siege, Abyss Boss") {
			t.Errorf("%s help lacks the Abyss events", l)
		}
	}

	// Rallies without a size never fill, so they don't count against how
	// many runs did.
	now := time.Now()
	s := summarize([]lfg.Run{
		{GuildID: "g1", DungeonID: "krao-cave", Status: lfg.StatusClosed, CreatedAt: now, FilledAt: now.Add(time.Minute)},
		{GuildID: "g1", DungeonID: "artifact-siege", Status: lfg.StatusClosed, CreatedAt: now, Unlimited: true, Players: 30},
	})
	if stats := jsonOf(t, b.statsPanel(s, 7, 1, nil, i18n.English)); !strings.Contains(stats, "Filled: **1** (100%)") ||
		!strings.Contains(stats, "Artifact Siege") {
		t.Errorf("stats:\n%s", stats)
	}
	s = summarize([]lfg.Run{{GuildID: "g1", DungeonID: "abyss-farm", Status: lfg.StatusOpen, CreatedAt: now, Unlimited: true}})
	if stats := jsonOf(t, b.statsPanel(s, 7, 1, nil, i18n.English)); strings.Contains(stats, "Filled") {
		t.Errorf("stats of unlimited rallies only:\n%s", stats)
	}
}
