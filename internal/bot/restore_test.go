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

// productionIcons draws cards as in production, where every role, class and
// interface emoji is an uploaded icon named by emoji.Sync.
func productionIcons(b *Bot) {
	icons := map[string]string{"leader": "<:lfg_leader_0a1b2c:1234567890123456789>"}
	for _, r := range b.game.Roles {
		icons[r.ID] = "<:lfg_" + r.ID + "_1a2b3c:1234567890123456789>"
	}
	for _, c := range b.game.Classes {
		icons[c.ID] = "<:lfg_" + c.ID + "_2b3c4d:1234567890123456789>"
	}
	for _, f := range b.game.Factions {
		icons[f.ID] = "<:lfg_" + f.ID + "_3c4d5e:1234567890123456789>"
	}
	b.game.UseIcons(icons)
	ui := map[string]string{}
	for _, id := range uiIcons {
		ui[iconPrefix+id] = "<:lfg_ui_" + id + "_4d5e6f:1234567890123456789>"
	}
	b.icons = newIconSet(ui)
}

// sentCard is a post's card the way Discord sends it along with a click.
func sentCard(t *testing.T, b *Bot, p lfg.Post, guildID, role string, l i18n.Lang) []discordgo.MessageComponent {
	t.Helper()
	raw, err := json.Marshal(b.icons.apply(b.postCard(p, guildID, role, l)))
	if err != nil {
		t.Fatal(err)
	}
	var m discordgo.Message
	if err := json.Unmarshal([]byte(`{"components":`+string(raw)+`}`), &m); err != nil {
		t.Fatal(err)
	}
	return m.Components
}

// lostLeader leads the party on the card that started all this.
const lostLeader = "100"

// lostParty is that party.
func lostParty() lfg.Post {
	start := time.Now().Add(75 * time.Hour).Truncate(time.Second)
	return lfg.Post{
		ID: "gadrd3", LeaderID: lostLeader, LeaderName: "DaveCZ", LeaderClass: "spiritmaster", LeaderRole: "dps", LeaderGS: 2793,
		GuildID: "g1", GuildName: "One", DungeonID: "abyssal-forge-ludra", ModeID: "conquest", Missing: 1,
		LookingFor: []string{"tank", "healer", "dps"}, Status: lfg.StatusOpen, StartAt: start,
		ExpiresAt: start.Add(3 * time.Hour), Note: "Čas si ještě upřesníme.",
		Accepted: []lfg.Player{
			{UserID: "101", Name: "Tomixus", ClassID: "templar", RoleID: "tank", GS: 2858, Char: "já", GuildID: "g1", GuildName: "One"},
			{UserID: "102", Name: "Truplik", ClassID: "templar", RoleID: "tank", GS: 2803, Char: "Truplik", GuildID: "g1", GuildName: "One"},
			{UserID: "103", Name: "mistep", ClassID: "ranger", RoleID: "dps", GS: 2800, GuildID: "g2", GuildName: "Tarot Club"},
			{UserID: "104", Name: "Medil", ClassID: "ranger", RoleID: "dps", GS: 2870, GuildID: "g2", GuildName: "Tarot Club"},
			{UserID: "105", Name: "MarAnt", ClassID: "chanter", RoleID: "dps", GS: 2900, GuildID: "g2", GuildName: "Tarot Club"},
			{UserID: "106", Name: "HachaCZ", ClassID: "ranger", RoleID: "dps", GS: 2968, GuildID: "g2", GuildName: "Tarot Club"},
			{UserID: "107", Name: "Hesttia", ClassID: "cleric", RoleID: "healer", GS: 2800, GuildID: "g1", GuildName: "One"},
			{UserID: "108", Name: "Cactusaak", ClassID: "cleric", RoleID: "healer", GS: 2900, GuildID: "g2", GuildName: "Tarot Club"},
		},
	}
}

// Every kind of card, as the bot draws it, reads back as the post it shows.
func TestCardsReadBack(t *testing.T) {
	start := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	tom := lfg.Player{UserID: "101", ClassID: "templar", RoleID: "tank", GS: 2500, Char: "Tomek", GuildID: "g1", GuildName: "One"}
	bob := lfg.Player{UserID: "202", Name: "B*b_ · — `x`", ClassID: "gladiator", RoleID: "dps", GS: 1234, GuildID: "g2", GuildName: "Two *&* Co"}
	amy := lfg.Player{UserID: "203", ClassID: "cleric", RoleID: "healer", GuildID: "g1", GuildName: "One"}
	base := lfg.Post{ID: "abc234", LeaderID: "200", LeaderChar: "LeaInGame", LeaderClass: "cleric", LeaderRole: "healer", LeaderGS: 3000,
		GuildID: "g1", GuildName: "One", DungeonID: "krao-cave", ModeID: "conquest", Missing: 3, LookingFor: []string{"tank", "ranger"},
		Status: lfg.StatusOpen, ExpiresAt: start}

	cases := map[string]func(p *lfg.Post){
		"plain": func(p *lfg.Post) {},
		"the card from the report": func(p *lfg.Post) {
			*p = lostParty()
		},
		"details": func(p *lfg.Post) {
			p.MinGS, p.StartAt, p.Faction = 2500, start, "elyos"
			p.Note = "*bold* _x_ ~y~ `z` | # > \\ @everyone <@1>\nsecond **line**\n> third"
			p.Accepted = []lfg.Player{tom}
			p.Missing = 2
		},
		"everyone waits": func(p *lfg.Post) {
			p.Accepted, p.Missing = []lfg.Player{tom, bob}, 1
			p.Applicants, p.Reserves = []lfg.Player{amy}, []lfg.Player{{UserID: "204", Name: "Rob", ClassID: "ranger", RoleID: "dps", GuildID: "g2", GuildName: "Two"}}
		},
		"players found outside": func(p *lfg.Post) {
			p.Accepted, p.Missing = []lfg.Player{tom}, 1
		},
		"nobody yet": func(p *lfg.Post) {},
		"mode with a dot": func(p *lfg.Post) {
			p.DungeonID, p.ModeID, p.MinGS = "deus-research-base", "stage-2", 1900
			p.LookingFor, p.Faction = []string{"any"}, "asmodian"
		},
		"full": func(p *lfg.Post) {
			p.Accepted, p.Missing, p.Status = []lfg.Player{tom, amy}, 0, lfg.StatusFull
			p.Reserves = []lfg.Player{{UserID: "204", Name: "Rob", ClassID: "ranger", RoleID: "dps", GuildID: "g2", GuildName: "Two"}}
			p.LookingFor = []string{"any"}
		},
		"leader who didn't pick": func(p *lfg.Post) {
			p.LeaderClass, p.LeaderRole, p.LeaderGS, p.LeaderChar = "", "", 0, ""
		},
		"rally": func(p *lfg.Post) {
			p.DungeonID, p.ModeID, p.Rally, p.Size, p.Missing = "artifact-siege", "lower-reshanta", true, 10, 0
			p.LookingFor, p.Faction, p.Server = []string{"any"}, "elyos", "Siel*"
			p.Accepted = []lfg.Player{tom, bob}
		},
		"rally without a limit or mode": func(p *lfg.Post) {
			p.DungeonID, p.ModeID, p.Rally, p.Size, p.Missing = "abyss-boss", "", true, 0, 0
			p.LookingFor = []string{"tank", "cleric"}
		},
		"full rally": func(p *lfg.Post) {
			p.DungeonID, p.ModeID, p.Rally, p.Size, p.Missing, p.Status = "abyss-boss", "", true, 3, 0, lfg.StatusFull
			p.LookingFor, p.Accepted = []string{"any"}, []lfg.Player{tom, amy}
		},
	}
	shared := goneBot(t, &fakeDiscord{})
	for name, change := range cases {
		for _, l := range i18n.Langs {
			for _, icons := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/icons=%v", name, l, icons), func(t *testing.T) {
					b := testBot(t)
					b.s, b.store = shared.s, shared.store
					if icons {
						productionIcons(b)
					}
					p := base
					change(&p)
					r, ok := b.readCard(sentCard(t, b, p, "g1", "", l), p.ID)
					if !ok {
						t.Fatal("card not read")
					}
					// What the bot would find out: the people named by name
					// here, and whoever it links on the card.
					named := map[string]string{}
					for _, pl := range p.People() {
						named[pl.Name] = pl.UserID
					}
					for _, pl := range []*lfg.Player{&r.leader} {
						pl.GuildID, pl.GuildName = "g1", "One"
					}
					for _, list := range [][]lfg.Player{r.post.Accepted, r.post.Applicants, r.post.Reserves} {
						for n := range list {
							if list[n].UserID == "" {
								list[n].UserID = named[list[n].Name]
							}
						}
					}
					got, left := b.restoredPost(r, "1234567890", "g1", time.Now())

					if len(left) != 0 {
						t.Errorf("left out %v", left)
					}
					if got.DungeonID != p.DungeonID || got.ModeID != p.ModeID || got.MinGS != p.MinGS || got.Rally != p.Rally || got.Size != p.Size ||
						got.Faction != p.Faction || got.Server != p.Server || got.Note != p.Note || got.Status != p.Status || got.Missing != p.Missing {
						t.Errorf("got  %+v\nwant %+v", got, p)
					}
					if !got.StartAt.Equal(p.StartAt) {
						t.Errorf("start %v, want %v", got.StartAt, p.StartAt)
					}
					if p.Status == lfg.StatusOpen && !slices.Equal(got.LookingFor, p.LookingFor) {
						t.Errorf("looking for %v, want %v", got.LookingFor, p.LookingFor)
					}
					if got.LeaderID != p.LeaderID || got.LeaderChar != p.LeaderChar || got.LeaderClass != p.LeaderClass ||
						got.LeaderRole != p.LeaderRole || got.LeaderGS != p.LeaderGS {
						t.Errorf("leader: got %+v, want %+v", got.Leader(), p.Leader())
					}
					for label, lists := range map[string][2][]lfg.Player{
						"party": {got.Accepted, p.Accepted}, "waiting": {got.Applicants, p.Applicants}, "reserves": {got.Reserves, p.Reserves},
					} {
						if !samePlayers(lists[0], lists[1]) {
							t.Errorf("%s: got %+v\nwant %+v", label, lists[0], lists[1])
						}
					}
					if !got.ExpiresAt.After(time.Now()) {
						t.Errorf("expires %v", got.ExpiresAt)
					}
					if p.StartAt.After(time.Now()) && !got.ExpiresAt.Equal(p.StartAt.Add(b.game.PostTTL())) {
						t.Errorf("expires %v, for a start at %v", got.ExpiresAt, p.StartAt)
					}
				})
			}
		}
	}
}

// samePlayers compares what a card shows of players, in any order: the roster
// goes by role.
func samePlayers(a, b []lfg.Player) bool {
	key := func(list []lfg.Player) []string {
		var out []string
		for _, pl := range list {
			out = append(out, fmt.Sprint(pl.UserID, "|", pl.Char, "|", pl.ClassID, "|", pl.RoleID, "|", pl.GS))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

// A Chanter healing shows as DPS beside a Cleric, which is all the card says.
func TestReadMemberTakesTheShownRole(t *testing.T) {
	b := testBot(t)
	p := lostParty()
	p.Accepted[4].RoleID = "healer"
	r, ok := b.readCard(sentCard(t, b, p, "g1", "", i18n.Czech), p.ID)
	if !ok {
		t.Fatal("card not read")
	}
	i := slices.IndexFunc(r.post.Accepted, func(pl lfg.Player) bool { return pl.Name == "MarAnt" })
	if i < 0 || r.post.Accepted[i].RoleID != "dps" {
		t.Errorf("%+v", r.post.Accepted)
	}
}

// A class with more than one role comes back in the one it played, like a
// Templar as DPS.
func TestReadMemberKeepsTheRolePlayed(t *testing.T) {
	b := testBot(t)
	p := lostParty()
	p.Accepted[0].RoleID = "dps"
	r, ok := b.readCard(sentCard(t, b, p, "g1", "", i18n.Czech), p.ID)
	if !ok {
		t.Fatal("card not read")
	}
	i := slices.IndexFunc(r.post.Accepted, func(pl lfg.Player) bool { return pl.UserID == p.Accepted[0].UserID })
	if i < 0 || r.post.Accepted[i].ClassID != "templar" || r.post.Accepted[i].RoleID != "dps" {
		t.Errorf("%+v", r.post.Accepted)
	}
}

func TestCardsThatCantBeRead(t *testing.T) {
	b := testBot(t)
	p := lostParty()
	if _, ok := b.readCard(sentCard(t, b, p, "g1", "", i18n.English), "another"); ok {
		t.Error("read the card of another post")
	}
	p.DungeonID = "no-such-dungeon"
	if _, ok := b.readCard(sentCard(t, b, p, "g1", "", i18n.English), p.ID); ok {
		t.Error("read a card of a dungeon no longer in the game")
	}
}

func TestEscapeReadsBack(t *testing.T) {
	for _, s := range []string{"plain", `a\b*c_d~e|f>g#h`, "`code`", "@everyone", `\*`, "Č·š — ž"} {
		if got := unescape(escape(s)); got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}

// restoreDiscord answers as Discord would about the party on the card: every
// member's name on their server, and a search by name.
func restoreDiscord() *fakeDiscord {
	sent := 0
	members := map[string]string{lostLeader: "DaveCZ", "101": "Tomixus", "102": "Truplik", "103": "mistep", "107": "Hesttia"}
	searches := map[string]string{"DaveCZ": lostLeader, "Medil": "104", "MarAnt": "105", "HachaCZ": "106", "Cactusaak": "108"}
	return &fakeDiscord{answer: func(call string) (int, string) {
		method, path, _ := strings.Cut(call, " ")
		switch {
		case call == callback:
			return http.StatusNoContent, ""
		case method == "GET" && strings.Contains(path, "/members/search"):
			var out []string
			for name, id := range searches {
				if strings.Contains(path, "query="+name) {
					out = append(out, fmt.Sprintf(`{"nick":%q,"user":{"id":%q,"username":"x"}}`, name, id))
					// Discord matches the start of the name.
					out = append(out, fmt.Sprintf(`{"nick":%q,"user":{"id":"other","username":"y"}}`, name+"2"))
				}
			}
			return http.StatusOK, "[" + strings.Join(out, ",") + "]"
		case method == "GET" && strings.Contains(path, "/members/"):
			id := path[strings.LastIndex(path, "/")+1:]
			if name, ok := members[id]; ok {
				return http.StatusOK, fmt.Sprintf(`{"nick":%q,"user":{"id":%q,"username":"x"}}`, name, id)
			}
			return http.StatusNotFound, `{"code":10007,"message":"Unknown Member"}`
		case strings.HasSuffix(path, "/threads"):
			return http.StatusBadRequest, `{"code":160004,"message":"A thread has already been created for this message"}`
		case method == "POST" && strings.HasSuffix(path, "/messages"):
			sent++
			return http.StatusOK, fmt.Sprintf(`{"id":"%d"}`, 500+sent)
		}
		return http.StatusOK, "{}"
	}}
}

// restoreBot knows the two servers of the party on the card.
func restoreBot(t *testing.T, f *fakeDiscord) *Bot {
	t.Helper()
	b := goneBot(t, f)
	productionIcons(b)
	for _, g := range []*discordgo.Guild{{ID: "g1", Name: "One"}, {ID: "g2", Name: "Tarot Club"}} {
		if err := b.s.State.GuildAdd(g); err != nil {
			t.Fatal(err)
		}
	}
	// One of the party is on both servers, so the card here links them.
	b.members.set("g1", "103", true, time.Now())
	return b
}

// clickAs is a click by a given user with the given permissions.
func clickAs(ic *discordgo.InteractionCreate, userID string, perms int64) *discordgo.InteractionCreate {
	ic.Member.User.ID, ic.Member.Permissions = userID, perms
	return ic
}

// The whole story of the card in the report: the bot has no post, someone
// clicks its card, which turns grey for everyone, and the leader puts it back
// with everyone on it.
func TestLostPostComesBackFromItsCard(t *testing.T) {
	t.Parallel()
	f := restoreDiscord()
	b := restoreBot(t, f)
	lost := lostParty()
	card := sentCard(t, b, lost, "g1", "role1", i18n.Czech)

	b.onInteraction(nil, click(t, customID(actJoin, lost.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card))
	got := answers(f)
	if len(got) != 1 || !strings.Contains(got[0], customID(actRestore, lost.ID)) {
		t.Fatalf("the card wasn't retired: %v", got)
	}
	var retired struct {
		Data struct {
			Components json.RawMessage `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(got[0]), &retired); err != nil {
		t.Fatal(err)
	}
	f.calls, f.bodies = nil, nil

	// Someone else can't.
	reopen := func(userID string, perms int64) *discordgo.InteractionCreate {
		return clickAs(clickJSON(t, customID(actRestore, lost.ID), discordgo.MessageFlagsIsComponentsV2, "cs", string(retired.Data.Components)), userID, perms)
	}
	b.onInteraction(nil, reopen("nobody", 0))
	if got := answers(f); len(got) != 1 || !strings.Contains(got[0], i18n.Czech.T("restore.not_allowed")) {
		t.Errorf("anyone can reopen it: %v", got)
	}
	if _, ok := b.store.Post(lost.ID); ok {
		t.Fatal("reopened by someone who may not")
	}
	f.calls, f.bodies = nil, nil

	// The leader can.
	b.onInteraction(nil, reopen(lostLeader, 0))
	b.syncWG.Wait()

	p, ok := b.store.Post(lost.ID)
	if !ok {
		t.Fatalf("not reopened: %v", f.calls)
	}
	if p.Status != lfg.StatusOpen || p.Missing != 1 || p.LeaderID != lostLeader || p.LeaderName != "DaveCZ" || p.GuildID != "g1" || p.GuildName != "One" ||
		!samePlayers(p.Accepted, lost.Accepted) || p.Note != lost.Note || !p.StartAt.Equal(lost.StartAt) {
		t.Errorf("reopened as %+v", p)
	}
	for _, pl := range p.Accepted {
		want := lost.Accepted[slices.IndexFunc(lost.Accepted, func(x lfg.Player) bool { return x.UserID == pl.UserID })]
		if pl.Name != want.Name || pl.GuildID != want.GuildID || pl.GuildName != want.GuildName {
			t.Errorf("player %+v, want %+v", pl, want)
		}
		if !slices.Contains(p.Pinged, pl.UserID) {
			t.Errorf("%s would be welcomed again", pl.Name)
		}
	}
	if len(p.Messages) != 1 || p.Messages[0].MessageID != "m1" || p.Messages[0].ChannelID != "c1" || p.Messages[0].PingRoleID != "role1" || !p.Messages[0].Thread {
		t.Errorf("card: %+v", p.Messages)
	}

	// The card is itself again, and the thread hears about it.
	f.mu.Lock()
	defer f.mu.Unlock()
	edit, note, done := "", "", ""
	for n, c := range f.calls {
		switch c {
		case "PATCH /channels/c1/messages/m1":
			edit = f.bodies[n]
		case "POST /channels/m1/messages":
			note = f.bodies[n]
		case "PATCH /webhooks/8/tok/messages/@original":
			done = f.bodies[n]
		}
	}
	for _, want := range []string{customID(actManage, lost.ID), customID(actJoin, lost.ID), `\u003c@\u0026role1\u003e`, fmt.Sprintf(`"accent_color":%d`, colorOpen)} {
		if !strings.Contains(edit, want) {
			t.Errorf("redrawn card lacks %s:\n%s", want, edit)
		}
	}
	if strings.Contains(edit, customID(actRestore, lost.ID)) || strings.Contains(edit, `"disabled":true`) {
		t.Errorf("the card still looks retired:\n%s", edit)
	}
	if !strings.Contains(note, i18n.Czech.T("thread.restored")) {
		t.Errorf("thread note: %s", note)
	}
	if want := i18n.Czech.T("restore.done", lost.ID, i18n.Czech.N(9, "n.player")); !strings.Contains(done, want) {
		t.Errorf("answer %s, want %s", done, want)
	}
	if strings.Contains(done, i18n.Czech.T("restore.rejoin", "")) {
		t.Errorf("says someone has to join again: %s", done)
	}
}

// Players nobody can find lose their place and are named, wherever anyone
// would look for them.
func TestPlayersWhoCantBeFoundJoinAgain(t *testing.T) {
	t.Parallel()
	f := restoreDiscord()
	answer := f.answer
	f.answer = func(call string) (int, string) {
		if strings.Contains(call, "query=Medil") || strings.Contains(call, "query=HachaCZ") {
			return http.StatusOK, "[]"
		}
		return answer(call)
	}
	b := restoreBot(t, f)
	lost := lostParty()
	card := sentCard(t, b, lost, "g1", "", i18n.Czech)

	b.onInteraction(nil, clickAs(click(t, customID(actRestore, lost.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card), lostLeader, 0))
	b.syncWG.Wait()

	p, ok := b.store.Post(lost.ID)
	if !ok {
		t.Fatalf("not reopened: %v", f.calls)
	}
	if len(p.Accepted) != 6 || p.Missing != 3 {
		t.Errorf("%d in the party, %d missing: %+v", len(p.Accepted), p.Missing, p.Accepted)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for n, c := range f.calls {
		if c == "POST /channels/m1/messages" || c == "PATCH /webhooks/8/tok/messages/@original" {
			if !strings.Contains(f.bodies[n], "**Medil**") || !strings.Contains(f.bodies[n], "**HachaCZ**") {
				t.Errorf("%s doesn't name them:\n%s", c, f.bodies[n])
			}
		}
	}
}

// Two members with the same name can't be told apart, so neither is taken for
// the player.
func TestFindByNameWantsOneMember(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"one":       {`[{"nick":"Bob","user":{"id":"1","username":"a"}}]`, "1"},
		"by global": {`[{"user":{"id":"2","username":"a","global_name":"Bob"}}]`, "2"},
		"by login":  {`[{"user":{"id":"3","username":"Bob"}}]`, "3"},
		"two":       {`[{"nick":"Bob","user":{"id":"1","username":"a"}},{"nick":"Bob","user":{"id":"2","username":"b"}}]`, ""},
		"a longer":  {`[{"nick":"Bobby","user":{"id":"1","username":"a"}}]`, ""},
		"none":      {`[]`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			b := goneBot(t, &fakeDiscord{answer: func(string) (int, string) { return http.StatusOK, c.body }})
			if got, _ := b.findByName("Bob", []string{"g1"}); got != c.want {
				t.Errorf("found %q, want %q", got, c.want)
			}
		})
	}
}

func TestWhoMayReopenALostPost(t *testing.T) {
	t.Parallel()
	mod := int64(discordgo.PermissionManageMessages)
	lost := lostParty()
	elsewhere := lostParty()
	elsewhere.GuildID, elsewhere.GuildName = "g2", "Tarot Club"
	for name, c := range map[string]struct {
		card   lfg.Post
		userID string
		perms  int64
		back   bool
	}{
		"the leader":                  {lost, lostLeader, 0, true},
		"a moderator of its server":   {lost, "x", mod, true},
		"a player":                    {lost, "101", 0, false},
		"a moderator of another":      {elsewhere, "x", mod, false},
		"its leader on another":       {elsewhere, lostLeader, 0, true},
		"an admin who can't moderate": {lost, "x", int64(discordgo.PermissionManageGuild), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := restoreDiscord()
			b := restoreBot(t, f)
			card := sentCard(t, b, c.card, "g1", "", i18n.English)
			b.onInteraction(nil, clickAs(click(t, customID(actRestore, c.card.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card), c.userID, c.perms))
			b.syncWG.Wait()
			if _, ok := b.store.Post(c.card.ID); ok != c.back {
				t.Errorf("reopened: %v, want %v", ok, c.back)
			}
		})
	}
}

func TestReopeningALostPostStopsWhereItShould(t *testing.T) {
	t.Parallel()
	lost := lostParty()
	reopen := func(t *testing.T, b *Bot, f *fakeDiscord, p lfg.Post) string {
		t.Helper()
		card := sentCard(t, b, p, "g1", "", i18n.English)
		b.onInteraction(nil, clickAs(click(t, customID(actRestore, p.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card), lostLeader, 0))
		b.syncWG.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		var said []string
		for n, c := range f.calls {
			if c == callback || c == "PATCH /webhooks/8/tok/messages/@original" {
				said = append(said, f.bodies[n])
			}
		}
		return strings.Join(said, "\n")
	}

	t.Run("the leader has too many runs", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		b := restoreBot(t, f)
		for n := range b.game.MaxRunsPerLeader {
			other := lostParty()
			other.ID, other.Accepted = "", nil
			other.ExpiresAt = time.Now().Add(time.Duration(n+1) * time.Hour)
			if _, err := b.store.CreatePost(other, b.game.MaxRunsPerLeader); err != nil {
				t.Fatal(err)
			}
		}
		if said := reopen(t, b, f, lost); !strings.Contains(said, customID(actManage, "")) {
			t.Errorf("said %s", said)
		}
		if _, ok := b.store.Post(lost.ID); ok {
			t.Error("reopened one run too many")
		}
	})
	t.Run("the leader is blocked here", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		b := restoreBot(t, f)
		if err := b.store.Block(lfg.Block{Scope: "g1", UserID: lostLeader, Name: "Dave", By: "mod"}); err != nil {
			t.Fatal(err)
		}
		if said := reopen(t, b, f, lost); !strings.Contains(said, i18n.English.T("restore.blocked")) {
			t.Errorf("said %s", said)
		}
		if _, ok := b.store.Post(lost.ID); ok {
			t.Error("reopened by a blocked leader")
		}
	})
	t.Run("a blocked player stays out", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		b := restoreBot(t, f)
		if err := b.store.Block(lfg.Block{Scope: "g1", UserID: "101", Name: "Tom", By: "mod"}); err != nil {
			t.Fatal(err)
		}
		reopen(t, b, f, lost)
		p, ok := b.store.Post(lost.ID)
		if !ok || p.Has("101") || p.Missing != 2 {
			t.Errorf("reopened as %+v", p)
		}
	})
	t.Run("the leader can't be found", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		answer := f.answer
		f.answer = func(call string) (int, string) {
			if strings.Contains(call, "query=") {
				return http.StatusOK, "[]"
			}
			return answer(call)
		}
		b := restoreBot(t, f)
		p := lostParty()
		p.GuildID, p.GuildName, p.LeaderName = "g2", "Tarot Club", "DaveCZ"
		card := sentCard(t, b, p, "g1", "", i18n.English)
		b.onInteraction(nil, clickAs(click(t, customID(actRestore, p.ID), discordgo.MessageFlagsIsComponentsV2, "en-US", card), "x", int64(discordgo.PermissionManageMessages)))
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.Contains(strings.Join(f.bodies, "\n"), i18n.English.T("restore.no_leader")) {
			t.Errorf("said %v", f.bodies)
		}
		if _, ok := b.store.Post(p.ID); ok {
			t.Error("reopened with no leader")
		}
	})
	t.Run("the card can't be read", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		b := restoreBot(t, f)
		p := lostParty()
		p.DungeonID = "no-such-dungeon"
		if said := reopen(t, b, f, p); !strings.Contains(said, strings.SplitN(i18n.English.T("restore.unreadable", ""), ".", 2)[0]) {
			t.Errorf("said %s", said)
		}
	})
	t.Run("it was reopened meanwhile", func(t *testing.T) {
		t.Parallel()
		f := restoreDiscord()
		b := restoreBot(t, f)
		if said := reopen(t, b, f, lost); !strings.Contains(said, "gadrd3") {
			t.Errorf("said %s", said)
		}
		f.calls, f.bodies = nil, nil
		if said := reopen(t, b, f, lost); !strings.Contains(said, i18n.English.T("restore.already")) {
			t.Errorf("said %s", said)
		}
	})
}

// A post that ended had its copies on the other servers deleted, so going back
// up takes it there again, quietly. With a lost database, those copies are
// still up, so it doesn't.
func TestReopeningAPostThatEndedSendsItToTheOtherServers(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{true, false} {
		t.Run(fmt.Sprintf("ended=%v", ended), func(t *testing.T) {
			t.Parallel()
			f := restoreDiscord()
			b := restoreBot(t, f)
			n, err := b.store.CreateNetwork("Friends", "g1", "One")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.store.JoinNetwork(n.Code, "g2", "Tarot Club"); err != nil {
				t.Fatal(err)
			}
			if err := b.store.SetGuild(lfg.GuildConfig{GuildID: "g2", GuildName: "Tarot Club", ChannelID: "c2", PingRoleID: "role2", Lang: "en"}); err != nil {
				t.Fatal(err)
			}
			lost := lostParty()
			if ended {
				// As the cleanup leaves it: a run in the stats, and nothing else.
				stored, _, err := b.store.RestorePost(lost, lfg.MessageRef{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}, 5)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := b.store.UpdatePost(stored.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) }); err != nil {
					t.Fatal(err)
				}
				b.store.DeletePost(stored.ID)
			}
			card := sentCard(t, b, lost, "g1", "", i18n.Czech)
			b.onInteraction(nil, clickAs(click(t, customID(actRestore, lost.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card), lostLeader, 0))
			b.syncWG.Wait()

			p, ok := b.store.Post(lost.ID)
			if !ok {
				t.Fatalf("not reopened: %v", f.calls)
			}
			if p.InGuild("g2") != ended || f.made("POST /channels/c2/messages") != ended {
				t.Errorf("on the other server: %v, want %v: %v", p.InGuild("g2"), ended, f.calls)
			}
			if ended {
				f.mu.Lock()
				defer f.mu.Unlock()
				for n, c := range f.calls {
					if c == "POST /channels/c2/messages" && strings.Contains(f.bodies[n], `\u003c@\u0026role2\u003e`) {
						t.Errorf("pinged the role:\n%s", f.bodies[n])
					}
				}
			}
		})
	}
}

// A card the bot forgot, of a post it still has, follows the post again once
// someone clicks it, and does what the click asked for.
func TestClickTakesBackACardTheBotForgot(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		before func(t *testing.T, b *Bot, id string)
		action string
		adopts bool
	}{
		"forgotten":                {func(*testing.T, *Bot, string) {}, actManage, true},
		"grey, with the post back": {func(*testing.T, *Bot, string) {}, actRestore, true},
		"its server took the post off": {func(t *testing.T, b *Bot, id string) {
			if _, err := b.store.UpdatePost(id, func(p *lfg.Post) error { p.Hide("g1"); return nil }); err != nil {
				t.Fatal(err)
			}
		}, actManage, false},
		"another card there": {func(t *testing.T, b *Bot, id string) {
			b.store.AddMessages(id, []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "other"}})
		}, actManage, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := restoreDiscord()
			b := restoreBot(t, f)
			p, _ := notePost()
			p.ExpiresAt = time.Now().Add(time.Hour)
			p, err := b.store.CreatePost(p, 5)
			if err != nil {
				t.Fatal(err)
			}
			c.before(t, b, p.ID)
			card := sentCard(t, b, p, "g1", "role1", i18n.Czech)

			b.onInteraction(nil, clickAs(click(t, customID(c.action, p.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card), "L", 0))
			b.syncWG.Wait()

			after, _ := b.store.Post(p.ID)
			ref, _ := after.MessageIn("g1")
			if adopted := ref.MessageID == "m1"; adopted != c.adopts {
				t.Errorf("took it back: %v, want %v: %+v", adopted, c.adopts, after.Messages)
			}
			if c.adopts && (ref.PingRoleID != "role1" || !f.made("PATCH /channels/c1/messages/m1")) {
				t.Errorf("card %+v, calls %v", ref, f.calls)
			}
			if got := answers(f); len(got) != 1 {
				t.Errorf("answered %d times", len(got))
			}
		})
	}

	// A thread note has the post's Manage button too, but isn't its card.
	f := restoreDiscord()
	b := restoreBot(t, f)
	p, _ := notePost()
	p.ExpiresAt = time.Now().Add(time.Hour)
	p, err := b.store.CreatePost(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	note := []discordgo.MessageComponent{b.joinNote(p, p.Accepted[0], false, "g1", i18n.Czech)}
	b.onInteraction(nil, clickAs(click(t, customID(actManage, p.ID), discordgo.MessageFlagsIsComponentsV2, "cs", note), "L", 0))
	if after, _ := b.store.Post(p.ID); len(after.Messages) != 0 {
		t.Errorf("took a note for a card: %+v", after.Messages)
	}
}
