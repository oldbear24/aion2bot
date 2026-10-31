package bot

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// Discord's limits for Components V2 messages.
const (
	maxComponents = 40
	maxTextLength = 4000
)

func testBot(t *testing.T) *Bot {
	t.Helper()
	game, err := config.LoadGame("../../game.json")
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{game: game}
}

// busyPost is the largest post the bot allows, with long names everywhere.
func busyPost(b *Bot) lfg.Post {
	long := strings.Repeat("W", 32)
	dungeon := b.game.Dungeons[0]
	p := lfg.Post{
		ID: "abc123", LeaderID: "1", LeaderName: long, GuildName: strings.Repeat("G", 100),
		DungeonID: dungeon.ID, ModeID: dungeon.Modes[len(dungeon.Modes)-1].ID, MinGS: 99999,
		LookingFor: []string{"tank", "healer", "dps", "gladiator", "chanter"}, Missing: 2,
		Start: strings.Repeat("s", 60), Note: strings.Repeat("note *with* _markdown_\n", 13),
		Status: lfg.StatusOpen, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	for n := range lfg.MaxApplicants {
		p.Applicants = append(p.Applicants, lfg.Player{UserID: fmt.Sprint(100 + n), Name: long, ClassID: "cleric", RoleID: "healer", GS: 99999, GuildID: "g2", GuildName: p.GuildName})
	}
	for n := range 9 {
		p.Accepted = append(p.Accepted, lfg.Player{UserID: fmt.Sprint(200 + n), Name: long, ClassID: "gladiator", RoleID: "tank", GS: 99999, GuildID: "g2", GuildName: p.GuildName})
	}
	return p
}

// measure counts every component (nested ones included, as Discord does)
// and the characters in all text displays.
func measure(t *testing.T, comps ...discordgo.MessageComponent) (count, textLen int) {
	t.Helper()
	raw, err := json.Marshal(comps)
	if err != nil {
		t.Fatal(err)
	}
	var tree []any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, c := range v {
				walk(c)
			}
		case map[string]any:
			count++
			if v["type"] == float64(discordgo.TextDisplayComponent) {
				textLen += len([]rune(v["content"].(string)))
			}
			for _, key := range []string{"components", "component", "accessory"} {
				if child, ok := v[key]; ok && child != nil {
					walk(child)
				}
			}
		}
	}
	walk(tree)
	return count, textLen
}

func checkSelects(t *testing.T, comps ...discordgo.MessageComponent) {
	t.Helper()
	raw, _ := json.Marshal(comps)
	var tree any
	json.Unmarshal(raw, &tree)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, c := range v {
				walk(c)
			}
		case map[string]any:
			if v["type"] == float64(discordgo.SelectMenuComponent) {
				opts, _ := v["options"].([]any)
				maxValues, _ := v["max_values"].(float64)
				if len(opts) == 0 || len(opts) > 25 || int(maxValues) > len(opts) {
					t.Errorf("select %v: %d options, max_values %v", v["custom_id"], len(opts), maxValues)
				}
				if id, _ := v["custom_id"].(string); len(id) > 100 {
					t.Errorf("custom_id too long: %s", id)
				}
			}
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(tree)
}

func TestPostCardFitsDiscordLimits(t *testing.T) {
	b := testBot(t)
	p := busyPost(b)
	for _, l := range i18n.Langs {
		for _, status := range []lfg.Status{lfg.StatusOpen, lfg.StatusFull, lfg.StatusClosed, lfg.StatusExpired} {
			p.Status = status
			count, textLen := measure(t, b.postCard(p, "g1", "999", l)...)
			if count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s card: %d components, %d characters", l, status, count, textLen)
			}
		}
	}
}

func TestManagePanelFitsDiscordLimits(t *testing.T) {
	b := testBot(t)
	for _, l := range i18n.Langs {
		p := busyPost(b)
		panel := b.managePanel(p, l)
		count, textLen := measure(t, panel)
		if count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s manage panel: %d components, %d characters", l, count, textLen)
		}
		checkSelects(t, panel)

		p.Applicants, p.Accepted = nil, nil
		checkSelects(t, b.managePanel(p, l))
	}
}

func TestDraftPanelFitsDiscordLimits(t *testing.T) {
	b := testBot(t)
	empty := draft{LookingFor: []string{config.AnyID}, Missing: 1}
	full := empty
	most := b.game.Dungeons[0]
	for _, d := range b.game.Dungeons {
		if len(d.Modes) > len(most.Modes) {
			most = d
		}
	}
	b.selectDungeon(&full, most.ID)
	full.StartInput, full.StartAt = "21.09.2026 10:15", time.Now().Add(time.Hour)
	full.Note = strings.Repeat("n", 300)
	full.Problem = "draft.need_looking"

	for _, l := range i18n.Langs {
		for name, d := range map[string]draft{"empty": empty, "full": full} {
			panel := b.draftPanel(d, l)
			count, textLen := measure(t, panel)
			if count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s draft panel: %d components, %d characters", l, name, count, textLen)
			}
			checkSelects(t, panel)
		}
	}
}

func TestModalsFitDiscordLimits(t *testing.T) {
	b := testBot(t)
	d := draft{LookingFor: []string{config.AnyID}, Missing: 1}
	b.selectDungeon(&d, "krao-cave")
	prof := lfg.Profile{ClassID: "gladiator", RoleID: "tank", GS: 99999}
	for _, lang := range i18n.Langs {
		for _, m := range []modal{b.detailsModal(d, lang), b.joinModal(busyPost(b), prof, lang), b.profileModal(prof, lang)} {
			if len(m.Components) > 5 || len([]rune(m.Title)) > 45 || len(m.CustomID) > 100 {
				t.Fatalf("%s modal %q has %d components", lang, m.Title, len(m.Components))
			}
			for _, c := range m.Components {
				l := c.(discordgo.Label)
				if len([]rune(l.Label)) > 45 || len([]rune(l.Description)) > 100 {
					t.Errorf("%s label %q is too long", lang, l.Label)
				}
				if ti, ok := l.Component.(discordgo.TextInput); ok && len([]rune(ti.Placeholder)) > 100 {
					t.Errorf("%s placeholder %q is too long", lang, ti.Placeholder)
				}
			}
			checkSelects(t, m.Components...)
		}
	}
}

func TestOtherPanelsFitDiscordLimits(t *testing.T) {
	b := testBot(t)
	p := busyPost(b)
	var open []lfg.Post
	for range 9 {
		open = append(open, p)
	}
	for _, l := range i18n.Langs {
		for name, panel := range map[string]discordgo.MessageComponent{
			"confirm delete": b.confirmDelete(p, l),
			"too many runs":  b.tooManyRuns(open, l),
			"help":           b.helpPanel(l, false),
			"admin help":     b.helpPanel(l, true),
		} {
			if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s: %d components, %d characters", l, name, count, textLen)
			}
		}
	}
}

func TestJoinFormStartsFromProfile(t *testing.T) {
	b := testBot(t)
	p := busyPost(b)
	m := b.joinModal(p, lfg.Profile{ClassID: "gladiator", RoleID: "tank", GS: 3150}, i18n.English)
	sel := m.Components[0].(discordgo.Label).Component.(modalSelect)
	var picked []string
	for _, o := range sel.Options {
		if o.Default {
			picked = append(picked, o.Value)
		}
	}
	if !slices.Equal(picked, []string{"gladiator|tank"}) {
		t.Errorf("pre-selected %v", picked)
	}
	if gs := m.Components[1].(discordgo.Label).Component.(discordgo.TextInput).Value; gs != "3150" {
		t.Errorf("gear score prefilled as %q", gs)
	}

	// A saved combination the post doesn't ask for isn't forced on the player.
	p.LookingFor = []string{"healer"}
	sel = b.joinModal(p, lfg.Profile{ClassID: "gladiator", RoleID: "tank"}, i18n.English).Components[0].(discordgo.Label).Component.(modalSelect)
	for _, o := range sel.Options {
		if o.Default {
			t.Errorf("%s pre-selected on a healer-only post", o.Value)
		}
	}
}

func TestCardLinksUsersWhereTheyAreMembers(t *testing.T) {
	b := testBot(t)
	p := lfg.Post{
		ID: "abc123", LeaderID: "L", LeaderName: "Lea", GuildID: "g1", GuildName: "One",
		DungeonID: "krao-cave", LookingFor: []string{config.AnyID}, Missing: 2, Status: lfg.StatusOpen,
		Applicants: []lfg.Player{
			{UserID: "A", Name: "Ann", ClassID: "cleric", RoleID: "healer", GuildID: "g1", GuildName: "One"},
			{UserID: "B", Name: "Bob", ClassID: "ranger", RoleID: "dps", GuildID: "g2", GuildName: "Two"},
		},
	}
	render := func(guildID string) string {
		var sb strings.Builder
		var walk func(c discordgo.MessageComponent)
		walk = func(c discordgo.MessageComponent) {
			switch v := c.(type) {
			case discordgo.Container:
				for _, cc := range v.Components {
					walk(cc)
				}
			case discordgo.Section:
				for _, cc := range v.Components {
					walk(cc)
				}
			case discordgo.TextDisplay:
				sb.WriteString(v.Content + "\n")
			}
		}
		for _, c := range b.postCard(p, guildID, "", i18n.English) {
			walk(c)
		}
		return sb.String()
	}
	home, away := render("g1"), render("g2")
	for _, want := range []string{`<@L>`, `<@A>`, `**Bob**`, `*Two*`} {
		if !strings.Contains(home, want) {
			t.Errorf("server one's copy lacks %s", want)
		}
	}
	for _, want := range []string{`**Lea**`, `**Ann**`, `<@B>`, `*One*`} {
		if !strings.Contains(away, want) {
			t.Errorf("server two's copy lacks %s", want)
		}
	}
	if strings.Contains(away, `<@L>`) || strings.Contains(home, `<@B>`) {
		t.Error("users are mentioned on a server they aren't from")
	}

	// Once the bot knows the leader is also on server two and Bob isn't on
	// server one, the leader is linked there too, the same as a player.
	now := time.Now()
	b.members.set("g2", "L", true, now)
	b.members.set("g1", "B", false, now)
	home, away = render("g1"), render("g2")
	if !strings.Contains(away, `<@L>`) || strings.Contains(away, `**Lea**`) {
		t.Errorf("leader who is a member of server two should be mentioned there:\n%s", away)
	}
	if !strings.Contains(home, `**Bob**`) || strings.Contains(home, `<@B>`) {
		t.Errorf("Bob isn't on server one:\n%s", home)
	}
}

func TestMemberCacheExpires(t *testing.T) {
	var c memberCache
	now := time.Now()
	if _, known := c.get("g", "u", now); known {
		t.Fatal("empty cache knows a member")
	}
	c.set("g", "u", true, now)
	if member, known := c.get("g", "u", now.Add(memberTTL/2)); !member || !known {
		t.Fatal("fresh entry lost")
	}
	if _, known := c.get("g", "u", now.Add(memberTTL+time.Minute)); known {
		t.Fatal("stale entry still trusted")
	}
	c.dropStale(now.Add(memberTTL + time.Minute))
	if len(c.entries) != 0 {
		t.Fatalf("stale entries kept: %v", c.entries)
	}
}

func TestSetupReport(t *testing.T) {
	all := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages |
		discordgo.PermissionCreatePublicThreads | discordgo.PermissionSendMessagesInThreads)
	if report, ready := setupReport(all, "c", nil, i18n.English); !ready || strings.ContainsAny(report, "❌⚠") {
		t.Errorf("all permissions: ready %v\n%s", ready, report)
	}
	// Both thread permissions are needed to ping in a thread.
	if report, ready := setupReport(all&^discordgo.PermissionSendMessagesInThreads, "c", nil, i18n.English); !ready ||
		!strings.Contains(report, "⚠️ **Create Public Threads + Send Messages in Threads**") {
		t.Errorf("missing thread permission should warn but still work: ready %v\n%s", ready, report)
	}
	if report, ready := setupReport(all&^discordgo.PermissionSendMessages, "c", nil, i18n.English); ready || !strings.Contains(report, "❌ **Send Messages**") {
		t.Errorf("missing send must block setup: ready %v\n%s", ready, report)
	}

	quiet := &discordgo.Role{ID: "r"}
	if report, _ := setupReport(all, "c", quiet, i18n.English); !strings.Contains(report, "isn't mentionable") {
		t.Errorf("unmentionable role without Mention Everyone:\n%s", report)
	}
	if report, _ := setupReport(all|discordgo.PermissionMentionEveryone, "c", quiet, i18n.English); !strings.Contains(report, "✅ New posts will notify") {
		t.Errorf("Mention Everyone lets the bot ping any role:\n%s", report)
	}
}

func TestThreadName(t *testing.T) {
	b := testBot(t)
	p := lfg.Post{DungeonID: "krao-cave", ModeID: "conquest", LeaderName: "Leader"}
	if got := b.threadName(p); got != "Krao Cave · Conquest · Leader" {
		t.Errorf("thread name %q", got)
	}
	p.LeaderName = strings.Repeat("Ž", 120)
	if got := b.threadName(p); utf8.RuneCountInString(got) > 100 {
		t.Errorf("thread name over Discord's 100 characters: %d", utf8.RuneCountInString(got))
	}
}

func TestDraftFollowsDungeonModes(t *testing.T) {
	b := testBot(t)
	d := draft{LookingFor: []string{config.AnyID}, Missing: 9}

	// The minimum gear score is optional, so picking a mode leaves it alone.
	b.selectDungeon(&d, "abyssal-forge-ludra")
	if d.ModeID != "conquest" || d.MinGS != 0 || d.Missing != 9 || b.gameMinGS(d) != 2800 {
		t.Fatalf("ludra: mode %q gs %d missing %d", d.ModeID, d.MinGS, d.Missing)
	}

	// A 5-player dungeon caps the missing count at 4.
	b.selectDungeon(&d, "krao-cave")
	if d.ModeID != "exploration" || d.Missing != 4 {
		t.Fatalf("krao: mode %q missing %d", d.ModeID, d.Missing)
	}
	d.MinGS = 2700
	b.selectMode(&d, "conquest-hard")
	if d.MinGS != 2700 || b.gameMinGS(d) != 2600 {
		t.Fatalf("krao hard: leader's gs %d, game's %d", d.MinGS, b.gameMinGS(d))
	}

	b.selectDungeon(&d, "dredgion-depot")
	if d.ModeID != "easy" || d.Missing != 3 {
		t.Fatalf("dredgion: mode %q missing %d", d.ModeID, d.Missing)
	}
	b.selectMode(&d, "hard")
	if b.gameMinGS(d) != 2000 || b.draftMaxMissing(d) != 3 {
		t.Fatalf("dredgion hard: gs %d max missing %d", b.gameMinGS(d), b.draftMaxMissing(d))
	}
	b.selectMode(&d, "no-such-mode")
	if d.ModeID != "hard" {
		t.Fatalf("unknown mode accepted: %q", d.ModeID)
	}
}

func TestParseGS(t *testing.T) {
	for in, want := range map[string]int{
		"3000": 3000, "3,000": 3000, "3 150": 3150, "3.2k": 3200, "4K": 4000, "GS 2800+": 2800, "3.000": 3000, "": 0,
	} {
		if got, err := parseGS(in); err != nil || got != want {
			t.Errorf("parseGS(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "-5", "1000000"} {
		if _, err := parseGS(in); err == nil {
			t.Errorf("parseGS(%q) should fail", in)
		}
	}
	// Czech players write decimals with a comma and thousands with a space.
	for in, want := range map[string]int{"3,2k": 3200, "4" + nbsp + "500": 4500} {
		if got, err := parseGS(in); err != nil || got != want {
			t.Errorf("parseGS(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

// A join form submission as Discord sends it, with inputs wrapped in labels.
const joinSubmit = `{
	"id": "1", "type": 5, "token": "t", "guild_id": "42",
	"member": {"user": {"id": "7", "username": "player", "global_name": "Player"}},
	"data": {"custom_id": "lfg:joinform:abc123", "components": [
		{"type": 18, "id": 1, "component": {"type": 3, "id": 2, "custom_id": "choice", "values": ["gladiator|tank"]}},
		{"type": 18, "id": 3, "component": {"type": 4, "id": 4, "custom_id": "gs", "value": "3,150"}}
	]}
}`

func TestModalValues(t *testing.T) {
	var i discordgo.Interaction
	if err := json.Unmarshal([]byte(joinSubmit), &i); err != nil {
		t.Fatal(err)
	}
	data := i.ModalSubmitData()
	if action, postID, ok := parseCustomID(data.CustomID); !ok || action != actJoinForm || postID != "abc123" {
		t.Fatalf("custom id: %q %q %v", action, postID, ok)
	}
	v := modalValues(data.Components)
	if first(v["choice"]) != "gladiator|tank" || first(v["gs"]) != "3,150" {
		t.Fatalf("unexpected values: %v", v)
	}
	if id, name := interactionUser(&i); id != "7" || name != "Player" {
		t.Fatalf("user: %s %s", id, name)
	}
}

func TestNormalizeLookingFor(t *testing.T) {
	b := testBot(t)
	for _, c := range []struct {
		name           string
		picked, before []string
		want           []string
	}{
		// Roles come first, then classes, both in config order; unknown IDs drop.
		{"order", []string{"cleric", "dps", "nope", "tank"}, nil, []string{"tank", "dps", "cleric"}},
		{"ticking Any class clears the rest", []string{"cleric", "tank", "any"}, []string{"cleric", "tank"}, []string{"any"}},
		{"Any class on its own", []string{"any"}, nil, []string{"any"}},
		{"a pick after Any class replaces it", []string{"any", "healer"}, []string{"any"}, []string{"healer"}},
		{"several picks after Any class", []string{"any", "tank", "ranger"}, []string{"any"}, []string{"tank", "ranger"}},
		{"nothing picked", nil, []string{"tank"}, nil},
	} {
		if got := b.normalizeLookingFor(c.picked, c.before); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDraftStartsWithNothingPicked(t *testing.T) {
	b := testBot(t)
	d := draft{Missing: 1}
	b.selectDungeon(&d, "krao-cave")
	panel := b.draftPanel(d, i18n.English)
	found := false
	for _, c := range panel.(discordgo.Container).Components {
		row, ok := c.(discordgo.ActionsRow)
		if !ok {
			continue
		}
		if sel, ok := row.Components[0].(discordgo.SelectMenu); ok && sel.CustomID == draftID(fieldLookingFor) {
			found = true
			for _, o := range sel.Options {
				if o.Default {
					t.Errorf("%s is pre-selected", o.Value)
				}
			}
		}
	}
	if !found {
		t.Fatal("no Looking for menu on the panel")
	}
	if !postDisabled(panel) {
		t.Error("Post must stay disabled until the leader says who they need")
	}
	d.LookingFor = []string{"tank"}
	if postDisabled(b.draftPanel(d, i18n.English)) {
		t.Error("Post should be enabled with a dungeon and a role chosen")
	}
}

// postDisabled finds the Post button on the /lfg panel.
func postDisabled(panel discordgo.MessageComponent) bool {
	for _, c := range panel.(discordgo.Container).Components {
		if row, ok := c.(discordgo.ActionsRow); ok {
			for _, rc := range row.Components {
				if btn, ok := rc.(discordgo.Button); ok && btn.CustomID == draftID(fieldPost) {
					return btn.Disabled
				}
			}
		}
	}
	return false
}

func TestJoinChoicesFollowRoles(t *testing.T) {
	b := testBot(t)
	labels := func(lookingFor ...string) []string {
		var out []string
		for _, c := range b.game.Choices(lookingFor) {
			out = append(out, c.Class.ID+"|"+c.Role.ID)
		}
		return out
	}
	if got := labels("tank"); !slices.Equal(got, []string{"gladiator|tank", "templar|tank"}) {
		t.Errorf("tank: %v", got)
	}
	// A wanted class brings all its roles; a wanted role brings all its classes.
	if got := labels("healer", "gladiator"); !slices.Equal(got, []string{"gladiator|tank", "cleric|healer", "chanter|healer", "gladiator|dps"}) {
		t.Errorf("healer + gladiator: %v", got)
	}
	if got := len(labels(config.AnyID)); got != 10 {
		t.Errorf("any: %d choices, want every class/role pair (10)", got)
	}

	for _, c := range []struct {
		lookingFor  []string
		class, role string
		fits        bool
	}{
		{[]string{"tank"}, "gladiator", "tank", true},
		{[]string{"tank"}, "gladiator", "dps", false},
		{[]string{"healer"}, "templar", "healer", false}, // templars can't heal
		{[]string{config.AnyID}, "templar", "dps", false},
		{[]string{"cleric"}, "cleric", "healer", true},
		{[]string{"cleric"}, "cleric", "", false},
	} {
		if got := b.game.Fits(c.lookingFor, c.class, c.role); got != c.fits {
			t.Errorf("Fits(%v, %s, %s) = %v", c.lookingFor, c.class, c.role, got)
		}
	}
}

func TestCustomIDRoundTrip(t *testing.T) {
	action, postID, ok := parseCustomID(customID(actAccept, "abc123"))
	if !ok || action != actAccept || postID != "abc123" {
		t.Fatalf("got %q %q %v", action, postID, ok)
	}
	if _, _, ok := parseCustomID("other:thing"); ok {
		t.Fatal("foreign custom ID accepted")
	}
}

func TestDungeonPartySizeLimitsMissing(t *testing.T) {
	b := testBot(t)
	for _, c := range []struct {
		dungeon, mode string
		want          int
	}{
		{"draupnir", "conquest-hard", 4},
		{"dredgion-depot", "hard", 3},
		{"abyssal-forge-ludra", "conquest", 9},
	} {
		d, ok := b.game.Dungeon(c.dungeon)
		if !ok {
			t.Fatalf("dungeon %s missing from game.json", c.dungeon)
		}
		if _, ok := d.Mode(c.mode); !ok {
			t.Fatalf("%s has no mode %s", c.dungeon, c.mode)
		}
		if got := d.MaxMissing(c.mode, b.game.MaxMissing); got != c.want {
			t.Errorf("%s %s: max missing %d, want %d", c.dungeon, c.mode, got, c.want)
		}
	}
}

func TestHelpFitsDiscordLimits(t *testing.T) {
	b := testBot(t)
	b.commandIDs = map[string]string{"lfg": "111", "lfg-network": "222"}
	panel := b.helpPanel(i18n.English, true)
	count, textLen := measure(t, panel)
	if count > maxComponents || textLen > maxTextLength {
		t.Errorf("help: %d components, %d characters", count, textLen)
	}
	if b.cmd("lfg") != "</lfg:111>" || b.cmd("lfg-network join") != "</lfg-network join:222>" || b.cmd("profile") != "`/profile`" {
		t.Errorf("command mentions: %s %s %s", b.cmd("lfg"), b.cmd("lfg-network join"), b.cmd("profile"))
	}
}

func TestOnlyAdminsSeeAdminCommands(t *testing.T) {
	b := testBot(t)
	for _, l := range i18n.Langs {
		admin, _ := json.Marshal(b.helpPanel(l, true))
		player, _ := json.Marshal(b.helpPanel(l, false))
		for _, cmd := range []string{"/lfg-setup", "/lfg-check", "/lfg-network", "/lfg-disable", "/help-public"} {
			if !strings.Contains(string(admin), cmd) {
				t.Errorf("%s admin help lacks %s", l, cmd)
			}
			if strings.Contains(string(player), cmd) {
				t.Errorf("%s player help shows %s", l, cmd)
			}
		}
	}

	member := func(perms int64) *discordgo.Interaction {
		return &discordgo.Interaction{Member: &discordgo.Member{Permissions: perms}}
	}
	for _, c := range []struct {
		name string
		i    *discordgo.Interaction
		want bool
	}{
		{"DM", &discordgo.Interaction{User: &discordgo.User{ID: "u"}}, false},
		{"player", member(discordgo.PermissionSendMessages), false},
		{"moderator", member(discordgo.PermissionManageMessages), false},
		{"manage server", member(discordgo.PermissionManageGuild), true},
		{"administrator", member(discordgo.PermissionAdministrator), true},
	} {
		if got := managesServer(c.i); got != c.want {
			t.Errorf("%s: managesServer %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCardSpeaksTheServersLanguage(t *testing.T) {
	b := testBot(t)
	p := lfg.Post{ID: "abc123", LeaderID: "L", LeaderName: "Lea", GuildID: "g1", DungeonID: "krao-cave",
		ModeID: "conquest-hard", MinGS: 2600, LookingFor: []string{"tank", config.AnyID}, Missing: 3, Status: lfg.StatusOpen}
	texts := func(l i18n.Lang) string {
		raw, _ := json.Marshal(b.postCard(p, "g1", "", l))
		return string(raw)
	}
	cs, en := texts(i18n.Czech), texts(i18n.English)
	for _, want := range []string{"Hledáme ještě 3", "Libovolné povolání", "Přidat se", "GS 2", "Vedoucí"} {
		if !strings.Contains(cs, want) {
			t.Errorf("Czech card lacks %q", want)
		}
	}
	for _, want := range []string{"Looking for 3 more", "Any class", "Join", "GS 2,600+", "Leader"} {
		if !strings.Contains(en, want) {
			t.Errorf("English card lacks %q", want)
		}
	}
	if report, _ := setupReport(0, "c", nil, i18n.Czech); !strings.Contains(report, "Odesílat zprávy") {
		t.Errorf("Czech permission report:\n%s", report)
	}
}

func TestParseStart(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	// Thursday 30 October 2026, 22:00 in Prague.
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, prague)
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, prague) }

	for in, want := range map[string]time.Time{
		"21.09.2027 10:15":   at(2027, 9, 21, 10, 15),
		"21. 9. 2027 10:15":  at(2027, 9, 21, 10, 15),
		"21.9.27 10:15":      at(2027, 9, 21, 10, 15),
		"2027-09-21 10:15":   at(2027, 9, 21, 10, 15),
		"1.11. 20:30":        at(2026, 11, 1, 20, 30),
		"1.11 20:30":         at(2026, 11, 1, 20, 30),
		"23:30":              at(2026, 10, 30, 23, 30), // still today
		"10:15":              at(2026, 10, 31, 10, 15), // already passed today, so tomorrow
		"9.30":               at(2026, 10, 31, 9, 30),
		"  31.12.2026 0:00 ": at(2026, 12, 31, 0, 0),
	} {
		got, err := parseStart(in, prague, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseStart(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := parseStart("", prague, now); err != nil || !got.IsZero() {
		t.Errorf("empty start: %v %v", got, err)
	}
	for _, in := range []string{"tomorrow", "31.02.2027 10:00", "21.09.2027 25:00", "21.09.2027 10:61", "21.13. 10:00", "21.09.2027"} {
		if _, err := parseStart(in, prague, now); err == nil {
			t.Errorf("parseStart(%q) should fail", in)
		}
	}

	// The same typed time means a different moment in another zone.
	ny, _ := time.LoadLocation("America/New_York")
	inPrague, _ := parseStart("21.09.2027 10:15", prague, now)
	inNY, _ := parseStart("21.09.2027 10:15", ny, now)
	if inNY.Sub(inPrague) != 6*time.Hour {
		t.Errorf("New York is 6 hours behind Prague, got %v", inNY.Sub(inPrague))
	}
}

func TestTimeZones(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	winter, summer := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if zoneLabel(prague, winter) != "CET (UTC+1)" || zoneLabel(prague, summer) != "CEST (UTC+2)" || zoneLabel(time.UTC, winter) != "UTC" {
		t.Errorf("labels: %q %q %q", zoneLabel(prague, winter), zoneLabel(prague, summer), zoneLabel(time.UTC, winter))
	}
	for locale, want := range map[string]string{"cs": "Europe/Prague", "en-GB": "Europe/London", "en-US": "America/New_York", "pt-BR": "America/Sao_Paulo", "xx": "UTC"} {
		if got := guessZone(locale); got != want {
			t.Errorf("guessZone(%q) = %q, want %q", locale, got, want)
		}
	}
	for _, name := range timeZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("time zone %s can't be loaded: %v", name, err)
		}
	}
	for _, l := range i18n.Langs {
		if opts := zoneOptions("Europe/Prague", l, winter); len(opts) != len(timeZones) || len(opts) > 25 {
			t.Errorf("%s: %d time zone options", l, len(opts))
		}
	}
}

func TestCardShowsStartAsTimestamp(t *testing.T) {
	b := testBot(t)
	start := time.Date(2026, 11, 1, 19, 30, 0, 0, time.UTC)
	p := lfg.Post{ID: "abc123", LeaderID: "L", DungeonID: "krao-cave", LookingFor: []string{"tank"}, Missing: 1,
		Status: lfg.StatusOpen, StartAt: start}
	raw, _ := json.Marshal(b.postCard(p, "g1", "", i18n.Czech))
	if !strings.Contains(string(raw), fmt.Sprintf("t:%d:f", start.Unix())) || !strings.Contains(string(raw), fmt.Sprintf("t:%d:R", start.Unix())) {
		t.Errorf("start isn't a Discord timestamp: %s", raw)
	}
	p.StartAt, p.Start = time.Time{}, "after reset"
	if raw, _ := json.Marshal(b.postCard(p, "g1", "", i18n.English)); !strings.Contains(string(raw), "after reset") {
		t.Error("posts from before the date field lose their start text")
	}
}
