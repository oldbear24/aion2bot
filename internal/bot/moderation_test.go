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

// clickBy is an interaction from a member of a server with the given
// permissions in the channel they clicked in.
func clickBy(userID, guildID string, perms int64) *discordgo.Interaction {
	return &discordgo.Interaction{GuildID: guildID, Member: &discordgo.Member{User: &discordgo.User{ID: userID}, Permissions: perms}}
}

// modNetwork links g1 (the owner), g2 and g3, and returns a post made on
// g2 with copies on all three.
func modNetwork(t *testing.T, b *Bot) lfg.Post {
	t.Helper()
	n, err := b.store.CreateNetwork("Alliance", "g1", "One")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"g2", "g3"} {
		if _, err := b.store.JoinNetwork(n.Code, g, g); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := notePost()
	p.GuildID, p.GuildName = "g2", "Two"
	p.Messages = []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}, {GuildID: "g2", ChannelID: "c2", MessageID: "m2"},
		{GuildID: "g3", ChannelID: "c3", MessageID: "m3"}}
	return p
}

func TestModerationAccess(t *testing.T) {
	b := storeBot(t)
	p := modNetwork(t, b)
	mod := int64(discordgo.PermissionManageMessages)
	for _, c := range []struct {
		name string
		i    *discordgo.Interaction
		want access
	}{
		{"leader", clickBy("L", "g3", 0), accessLeader},
		{"leader in a DM", &discordgo.Interaction{User: &discordgo.User{ID: "L"}}, accessLeader},
		{"player", clickBy("T", "g2", discordgo.PermissionSendMessages), accessNone},
		{"moderator in a DM", &discordgo.Interaction{User: &discordgo.User{ID: "M"}}, accessNone},
		{"moderator of another server", clickBy("M", "g3", mod), accessMod},
		{"administrator of another server", clickBy("M", "g3", discordgo.PermissionAdministrator), accessMod},
		{"manage server only", clickBy("M", "g3", discordgo.PermissionManageGuild), accessNone},
		{"moderator of the post's server", clickBy("M", "g2", mod), accessInCharge},
		{"moderator of the owner server", clickBy("M", "g1", mod), accessInCharge},
		{"moderator outside the network", clickBy("M", "g9", mod), accessMod},
	} {
		if got := b.access(c.i, p); got != c.want {
			t.Errorf("%s: access %d, want %d", c.name, got, c.want)
		}
	}

	// A plain moderator removes the post here and blocks; the moderators in
	// charge also close, delete and hand over; the owner blocks network-wide.
	for _, c := range []struct {
		name                    string
		guild                   string
		acc                     access
		wantButtons, notButtons []string
		wantSelects, notSelects []string
	}{
		{"moderator", "g3", accessMod, []string{actHide}, []string{actClose, actDelete}, []string{actBlock},
			[]string{actNetBlock, actHandTo}},
		{"in charge", "g2", accessInCharge, []string{actHide, actClose, actDelete}, nil, []string{actBlock, actHandTo},
			[]string{actNetBlock}},
		{"owner", "g1", accessInCharge, []string{actHide, actClose, actDelete}, nil, []string{actBlock, actNetBlock, actHandTo}, nil},
	} {
		panel := b.modPanel(p, c.acc, c.guild, "", i18n.English)
		buttons, selects := buttonsOf(panel), selectIDs(t, panel)
		for _, a := range c.wantButtons {
			if _, ok := buttons[customID(a, p.ID)]; !ok {
				t.Errorf("%s panel lacks %s", c.name, a)
			}
		}
		for _, a := range c.notButtons {
			if _, ok := buttons[customID(a, p.ID)]; ok {
				t.Errorf("%s panel offers %s", c.name, a)
			}
		}
		for _, a := range c.wantSelects {
			if !slices.Contains(selects, customID(a, p.ID)) {
				t.Errorf("%s panel lacks the %s menu", c.name, a)
			}
		}
		for _, a := range c.notSelects {
			if slices.Contains(selects, customID(a, p.ID)) {
				t.Errorf("%s panel offers the %s menu", c.name, a)
			}
		}
	}
	// The leader gets their own panel, a moderator the moderation panel.
	if _, ok := buttonsOf(b.panelFor(clickBy("L", "g3", mod), p))[customID(actEdit, p.ID)]; !ok {
		t.Error("the leader doesn't get the manage panel")
	}
	if _, ok := buttonsOf(b.panelFor(clickBy("M", "g3", mod), p))[customID(actHide, p.ID)]; !ok {
		t.Error("a moderator doesn't get the moderation panel")
	}

	// The biggest posts fit, in every language, with the leader first in
	// the block menus.
	big := busyPost(iconBot(t))
	big.GuildID = "g2"
	big.Messages = p.Messages
	for _, l := range i18n.Langs {
		for _, guild := range []string{"g1", "g2", "g3"} {
			panel := b.modPanel(big, accessInCharge, guild, "✅ "+strings.Repeat("x", 300), l)
			if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
				t.Errorf("%s moderation panel on %s: %d components, %d characters", l, guild, count, textLen)
			}
			checkSelects(t, panel)
		}
	}
	if raw := jsonOf(t, b.modPanel(big, accessMod, "g3", "", i18n.English)); !strings.Contains(raw, `"value":"1"`) {
		t.Error("the block menu doesn't offer the leader")
	}
}

func TestBlocksKeepPostsAway(t *testing.T) {
	b := storeBot(t)
	p := modNetwork(t, b)
	if !b.welcomeIn(p, "g3") {
		t.Fatal("a post is shown before anyone blocks or hides it")
	}
	p.Hide("g3")
	if b.welcomeIn(p, "g3") || !b.welcomeIn(p, "g1") {
		t.Error("a hidden post shows on that server, or not on the others")
	}
	b.store.Block(lfg.Block{Scope: "g1", UserID: p.LeaderID, Name: p.LeaderName, By: "M"})
	if b.welcomeIn(p, "g1") || b.blockedFrom(p, "T", "g1") || !b.blockedFrom(lfg.Post{GuildID: "g1"}, p.LeaderID, "g2") {
		t.Error("a blocked leader's post shows there, or the block doesn't keep them off its runs")
	}
	if !b.blockedFrom(p, p.LeaderID, "g1") || b.blockedFrom(p, p.LeaderID, "g2") {
		t.Error("a player blocked on g1 can't join from g2, or can from g1")
	}

	// /lfg-blocks lists the server's blocks, and the network's on the owner
	// server.
	n, _ := b.store.NetworkOf("g1")
	b.store.Block(lfg.Block{Scope: lfg.NetworkScope(n.ID), UserID: "S", Name: "Spammer", By: "M"})
	if got := b.blockScopes("g1"); !slices.Equal(got, []string{"g1", lfg.NetworkScope(n.ID)}) {
		t.Errorf("owner manages %v", got)
	}
	if got := b.blockScopes("g3"); !slices.Equal(got, []string{"g3"}) {
		t.Errorf("member manages %v", got)
	}
	owner := jsonOf(t, b.blocksPanel("g1", "", i18n.English))
	if !strings.Contains(owner, "Spammer") || !strings.Contains(owner, "**Lea**") || !strings.Contains(owner, "in the whole network") {
		t.Errorf("owner's blocks:\n%s", owner)
	}
	if member := jsonOf(t, b.blocksPanel("g3", "", i18n.English)); strings.Contains(member, "Spammer") ||
		!strings.Contains(member, "Nobody is blocked") {
		t.Errorf("member's blocks:\n%s", member)
	}
	for n := range 40 {
		b.store.Block(lfg.Block{Scope: "g1", UserID: fmt.Sprint("u", n), Name: strings.Repeat("N", 32), By: "123456789012345678",
			At: time.Now().Add(-time.Duration(n) * time.Minute)})
	}
	for _, l := range i18n.Langs {
		panel := b.blocksPanel("g1", l.T("blocks.lifted", 3), l)
		if count, textLen := measure(t, panel); count > maxComponents || textLen > maxTextLength {
			t.Errorf("%s blocks panel: %d components, %d characters", l, count, textLen)
		}
		checkSelects(t, panel)
	}
}
