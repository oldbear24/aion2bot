package bot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// storeBot is a test bot with a database, for features that read profiles.
func storeBot(t *testing.T) *Bot {
	t.Helper()
	b := testBot(t)
	store, err := lfg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b.store = store
	return b
}

// jsonOf is a component as Discord gets it, with mentions left readable:
// discordgo's components escape < and > however they are encoded, so the
// JSON is decoded and written again without that.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestFullCardTakesReserves(t *testing.T) {
	b := testBot(t)
	p := lfg.Post{ID: "abc123", LeaderID: "L", LeaderName: "Lea", LeaderChar: "Lunaria", GuildID: "g1", DungeonID: "krao-cave",
		LookingFor: []string{config.AnyID}, Status: lfg.StatusFull,
		Accepted: []lfg.Player{{UserID: "A", Name: "Ann", ClassID: "cleric", RoleID: "healer", GuildID: "g1", Char: "Annie"}},
		Reserves: []lfg.Player{{UserID: "R", Name: "Rex", ClassID: "ranger", RoleID: "dps", GuildID: "g1"}}}
	for _, l := range i18n.Langs {
		card := jsonOf(t, b.postCard(p, "g1", "", l))
		for _, want := range []string{l.T("card.join_reserve"), l.T("roster.reserves", 1), "`Lunaria`", "`Annie`", "<@R>"} {
			if !strings.Contains(card, want) {
				t.Errorf("%s full card lacks %q:\n%s", l, want, card)
			}
		}
	}

	// The leader can accept the reserve once a slot is free.
	p.Status, p.Missing = lfg.StatusOpen, 1
	panel := jsonOf(t, b.managePanel(p, i18n.English))
	if !strings.Contains(panel, customID(actAccept, p.ID)) || !strings.Contains(panel, "🪑 Rex") {
		t.Errorf("manage panel doesn't offer the reserve:\n%s", panel)
	}
	if card := jsonOf(t, b.postCard(p, "g1", "", i18n.English)); strings.Contains(card, "Join as reserve") {
		t.Error("a post with room takes players, not reserves")
	}
}

func TestPromotedReserveIsWelcomedOnce(t *testing.T) {
	p := lfg.Post{LeaderID: "L", Missing: 1, Status: lfg.StatusOpen}
	p.AddApplicant(lfg.Player{UserID: "a", RoleID: "healer"})
	p.Accept([]string{"a"})
	for _, id := range []string{"r1", "r2"} {
		p.AddApplicant(lfg.Player{UserID: id, RoleID: "healer"})
	}

	// Removing a party member and the reserve who took their slot at once
	// welcomes only the next reserve.
	var removed []lfg.Removal
	for _, id := range []string{"a", "r1"} {
		r, _ := p.Remove(id)
		removed = append(removed, r)
	}
	if got := firstPings(&p, removed...); len(got) != 1 || got[0].UserID != "r2" {
		t.Fatalf("welcomed %+v", got)
	}
	if got := firstPings(&p, removed...); len(got) != 0 {
		t.Fatalf("welcomed twice: %+v", got)
	}
}

func TestCleanChar(t *testing.T) {
	for in, want := range map[string]string{
		"  Lunaria ":            "Lunaria",
		"Lu`na`ria":             "Lunaria",
		"Two\nlines\there":      "Two lines here",
		strings.Repeat("x", 30): strings.Repeat("x", maxChar-1) + "…",
		"":                      "",
	} {
		if got := cleanChar(in); got != want {
			t.Errorf("cleanChar(%q) = %q, want %q", in, got, want)
		}
	}
	if charSuffix("") != "" || charSuffix("Lea") != " · `Lea`" {
		t.Errorf("charSuffix: %q", charSuffix("Lea"))
	}
}

func TestProfileAsksForCharacterAndReminders(t *testing.T) {
	b := testBot(t)
	m := b.profileModal(lfg.Profile{Char: "Lunaria", SkipReminders: true}, i18n.English)
	values := map[string]discordgo.MessageComponent{}
	for _, c := range m.Components {
		l := c.(discordgo.Label)
		switch v := l.Component.(type) {
		case discordgo.TextInput:
			values[v.CustomID] = v
		case modalSelect:
			values[v.CustomID] = v
		}
	}
	if values["char"].(discordgo.TextInput).Value != "Lunaria" {
		t.Errorf("character name not filled in: %+v", values["char"])
	}
	for _, o := range values["remind"].(modalSelect).Options {
		if o.Default != (o.Value == "off") {
			t.Errorf("reminder option %s default %v", o.Value, o.Default)
		}
	}

	join := b.joinModal(busyPost(b), lfg.Profile{Char: "Lunaria"}, i18n.English)
	if ti := join.Components[2].(discordgo.Label).Component.(discordgo.TextInput); ti.CustomID != "char" || ti.Value != "Lunaria" {
		t.Errorf("join form character: %+v", ti)
	}
}

func TestCanJoinFollowsProfile(t *testing.T) {
	b := testBot(t)
	p := lfg.Post{ID: "p", LeaderID: "L", DungeonID: "krao-cave", LookingFor: []string{"healer"}, MinGS: 2600, Missing: 1,
		Status: lfg.StatusOpen}
	for _, c := range []struct {
		name   string
		user   string
		prof   lfg.Profile
		change func(p *lfg.Post)
		want   bool
	}{
		{"no profile", "u", lfg.Profile{}, nil, true},
		{"healer class", "u", lfg.Profile{ClassID: "cleric", GS: 2700}, nil, true},
		// A Chanter can heal, even if they saved DPS as their main role.
		{"class with a fitting role", "u", lfg.Profile{ClassID: "chanter", RoleID: "dps"}, nil, true},
		{"wrong class", "u", lfg.Profile{ClassID: "ranger"}, nil, false},
		{"gear score too low", "u", lfg.Profile{ClassID: "cleric", GS: 2500}, nil, false},
		{"leader", "L", lfg.Profile{}, nil, false},
		{"already on it", "u", lfg.Profile{}, func(p *lfg.Post) { p.Applicants = []lfg.Player{{UserID: "u"}} }, false},
		{"full takes reserves", "u", lfg.Profile{}, func(p *lfg.Post) { p.Status = lfg.StatusFull }, true},
		{"closed", "u", lfg.Profile{}, func(p *lfg.Post) { p.Status = lfg.StatusClosed }, false},
	} {
		post := p
		if c.change != nil {
			c.change(&post)
		}
		if got := b.canJoin(post, c.user, c.prof); got != c.want {
			t.Errorf("%s: canJoin %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSortRuns(t *testing.T) {
	now := time.Now()
	posts := []lfg.Post{
		{ID: "full-soon", Status: lfg.StatusFull, StartAt: now.Add(time.Minute)},
		{ID: "later", Status: lfg.StatusOpen, StartAt: now.Add(2 * time.Hour)},
		{ID: "when-full", Status: lfg.StatusOpen},
		{ID: "soon", Status: lfg.StatusOpen, StartAt: now.Add(30 * time.Minute)},
	}
	sortRuns(posts, now)
	var ids []string
	for _, p := range posts {
		ids = append(ids, p.ID)
	}
	if want := []string{"when-full", "soon", "later", "full-soon"}; !slices.Equal(ids, want) {
		t.Errorf("order %v, want %v", ids, want)
	}
}

func TestFindPanel(t *testing.T) {
	b := iconBot(t)
	p := busyPost(b)
	p.GuildID, p.Messages = "g1", []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}}
	var found []lfg.Post
	for n := range 25 {
		p.ID = fmt.Sprint("post", n)
		found = append(found, p)
	}
	for _, l := range i18n.Langs {
		for name, panel := range map[string]discordgo.MessageComponent{
			"many":       b.findPanel(found, "g1", lfg.Profile{ClassID: "cleric", GS: 3000}, l),
			"none":       b.findPanel(nil, "g1", lfg.Profile{}, l),
			"no profile": b.findPanel(found[:1], "g2", lfg.Profile{}, l),
		} {
			count, textLen := measure(t, panel)
			if count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s find panel: %d components, %d characters", l, name, count, textLen)
			}
			checkSelects(t, panel)
		}
	}
	panel := jsonOf(t, b.findPanel(found[:1], "g2", lfg.Profile{}, i18n.English))
	if !strings.Contains(panel, "https://discord.com/channels/g1/c1/m1") || !strings.Contains(panel, idFindJoin) {
		t.Errorf("find panel links the leader's copy and offers to join:\n%s", panel)
	}
}

func TestAlerts(t *testing.T) {
	b := storeBot(t)
	p := lfg.Post{ID: "p", LeaderID: "L", LeaderName: "Lea", GuildID: "g1", DungeonID: "krao-cave", ModeID: "conquest-hard",
		LookingFor: []string{"healer"}, MinGS: 2600, Missing: 2, Status: lfg.StatusOpen,
		Messages: []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}}}
	alert := lfg.Alert{UserID: "u", Dungeons: []string{"krao-cave", "draupnir"}, Choices: []string{"ranger|dps", "chanter|healer"}}

	if !b.alertMatches(alert, p) {
		t.Error("a Chanter healer alert matches a post looking for healers")
	}
	other := p
	other.DungeonID = "draupnir"
	other.LookingFor = []string{"tank"}
	if b.alertMatches(alert, other) {
		t.Error("nobody in the alert tanks")
	}
	other = p
	other.DungeonID = "fire-temple"
	if b.alertMatches(alert, other) {
		t.Error("fire temple isn't among the alert's dungeons")
	}
	b.store.SetProfile(lfg.Profile{UserID: "u", ClassID: "chanter", RoleID: "healer", GS: 2500})
	if b.alertMatches(alert, p) {
		t.Error("the player's gear score is below the post's minimum")
	}

	for _, l := range i18n.Langs {
		for name, panel := range map[string]discordgo.MessageComponent{
			"on":  b.alertPanel(lfg.Alert{Dungeons: []string{"krao-cave"}, Choices: alert.Choices}, lfg.Profile{GS: 3000}, l),
			"off": b.alertPanel(lfg.Alert{}, lfg.Profile{}, l),
			"dm":  b.alertDM(busyPost(b), p.Messages[0], l),
		} {
			count, textLen := measure(t, panel)
			if count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s %s alert: %d components, %d characters", l, name, count, textLen)
			}
			checkSelects(t, panel)
		}
	}
	on := jsonOf(t, b.alertPanel(lfg.Alert{Dungeons: []string{"krao-cave"}, Choices: []string{"chanter|healer"}}, lfg.Profile{}, i18n.English))
	if !strings.Contains(on, "Alerts are on") || !strings.Contains(on, "Krao Cave") {
		t.Errorf("alert panel:\n%s", on)
	}
	if dm := jsonOf(t, b.alertDM(p, p.Messages[0], i18n.Czech)); !strings.Contains(dm, "https://discord.com/channels/g1/c1/m1") ||
		!strings.Contains(dm, "Nový nábor") {
		t.Errorf("Czech alert DM:\n%s", dm)
	}
}

func TestStats(t *testing.T) {
	now := time.Now()
	runs := []lfg.Run{
		{GuildID: "g1", DungeonID: "krao-cave", ModeID: "conquest-hard", Status: lfg.StatusClosed, CreatedAt: now, FilledAt: now.Add(10 * time.Minute), Players: 4},
		{GuildID: "g1", DungeonID: "krao-cave", ModeID: "conquest-hard", Status: lfg.StatusExpired, CreatedAt: now, FilledAt: now.Add(30 * time.Minute), Players: 4},
		{GuildID: "g2", DungeonID: "krao-cave", ModeID: "conquest", Status: lfg.StatusExpired, CreatedAt: now, FilledAt: now.Add(90 * time.Minute), Players: 4},
		{GuildID: "g2", DungeonID: "fire-temple", ModeID: "conquest", Status: lfg.StatusOpen, CreatedAt: now, Players: 1},
	}
	s := summarize(runs)
	switch {
	case s.Posted != 4 || s.Open != 1 || s.Filled != 3 || s.Players != 13:
		t.Fatalf("counts %+v", s)
	case s.FillTime != 30*time.Minute:
		t.Fatalf("median fill time %v", s.FillTime)
	case len(s.Dungeons) != 2 || s.Dungeons[0] != (tally{ID: "krao-cave", N: 3, Mode: "conquest-hard"}):
		t.Fatalf("dungeons %+v", s.Dungeons)
	case len(s.Guilds) != 2 || s.Guilds[0].ID != "g1" || s.Guilds[0].N != 2:
		t.Fatalf("guilds %+v", s.Guilds)
	}

	b := storeBot(t)
	for _, l := range i18n.Langs {
		for _, days := range statsPeriods {
			panel := b.statsPanel(s, days, 3, map[string]string{"g1": "One"}, l)
			if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s stats: %d components, %d characters", l, count, textLen)
			}
			if raw := jsonOf(t, panel); strings.Contains(raw, "stats.period") {
				t.Errorf("%s stats for %d days lacks a period name", l, days)
			}
		}
	}
	panel := jsonOf(t, b.statsPanel(s, 7, 3, map[string]string{"g1": "One"}, i18n.English))
	for _, want := range []string{"Filled: **3** (75%)", "30 min", "Krao Cave** — 3 runs · mostly Conquest [Hard]", "By server", "• One — 2 runs", "• g2 — 2 runs"} {
		if !strings.Contains(panel, want) {
			t.Errorf("stats lack %q:\n%s", want, panel)
		}
	}
	if alone := jsonOf(t, b.statsPanel(summarize(nil), 7, 1, nil, i18n.Czech)); !strings.Contains(alone, "nevznikl") {
		t.Errorf("empty stats:\n%s", alone)
	}
	if duration(150*time.Minute, i18n.English) != "2 h 30 min" || duration(10*time.Second, i18n.English) != "1 min" {
		t.Errorf("durations %q %q", duration(150*time.Minute, i18n.English), duration(10*time.Second, i18n.English))
	}
}

func TestCommandsFitDiscordLimits(t *testing.T) {
	b := testBot(t)
	commands := commandList(b.game)
	if len(commands) > 100 {
		t.Fatalf("%d commands", len(commands))
	}
	var walk func(opts []*discordgo.ApplicationCommandOption)
	walk = func(opts []*discordgo.ApplicationCommandOption) {
		for _, o := range opts {
			if len(o.Choices) > 25 || len(o.Description) > 100 || len(o.Description) == 0 {
				t.Errorf("option %s: %d choices, description %q", o.Name, len(o.Choices), o.Description)
			}
			for _, c := range o.Choices {
				if len(c.Name) == 0 || len(c.Name) > 100 {
					t.Errorf("option %s: choice name %q", o.Name, c.Name)
				}
			}
			walk(o.Options)
		}
	}
	for _, c := range commands {
		if len(c.Description) == 0 || len(c.Description) > 100 {
			t.Errorf("/%s: description %q", c.Name, c.Description)
		}
		walk(c.Options)
	}
}
