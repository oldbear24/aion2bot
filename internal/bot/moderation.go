package bot

import (
	"errors"
	"log"
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// Posts travel to every linked server, but only their leader manages them.
// Moderators (Manage Messages) keep their own server in order:
//
//   - On any server a post shows on, they can take it off that server, and
//     block players there: their runs no longer arrive, and they can't post
//     runs or join them from there, or join runs posted there.
//   - Moderators of the server a post came from, and of the network's owner
//     server, are in charge of it: they can also close or delete it, and
//     hand it to a party member when its leader is gone.
//   - Moderators of the owner server can block players in the whole
//     network.
//
// /lfg-blocks lists the blocks and lifts them.

// access is what someone who clicked on a post may do with it.
type access int

const (
	accessNone access = iota
	// accessMod: a moderator of the server they clicked on.
	accessMod
	// accessInCharge: a moderator of the post's server or of the network
	// owner server.
	accessInCharge
	accessLeader
)

// errUnchanged leaves a stored post as it was.
var errUnchanged = errors.New("nothing to change")

// The /lfg-blocks panel uses "lfgb:<field>".
const (
	blocksPrefix  = "lfgb:"
	blocksUnblock = "unblock"
)

// moderates reports whether the user may moderate the channel the
// interaction comes from. Discord gives an interaction the member's
// permissions there.
func moderates(i *discordgo.Interaction) bool {
	return i.GuildID != "" && i.Member != nil &&
		i.Member.Permissions&(discordgo.PermissionManageMessages|discordgo.PermissionAdministrator) != 0
}

func (b *Bot) access(i *discordgo.Interaction, p lfg.Post) access {
	userID, _ := interactionUser(i)
	switch {
	case userID == p.LeaderID:
		return accessLeader
	case !moderates(i):
		return accessNone
	case i.GuildID == p.GuildID || b.ownsNetworkOf(i.GuildID, p.GuildID):
		return accessInCharge
	}
	return accessMod
}

// ownsNetworkOf reports whether a server owns the network another server
// is in.
func (b *Bot) ownsNetworkOf(ownerID, guildID string) bool {
	n, ok := b.store.NetworkOf(ownerID)
	return ok && n.OwnerGuildID == ownerID &&
		slices.ContainsFunc(n.Members, func(m lfg.NetworkMember) bool { return m.GuildID == guildID })
}

// ownsNetwork reports whether a server owns a network, and which.
func (b *Bot) ownsNetwork(guildID string) (lfg.Network, bool) {
	n, ok := b.store.NetworkOf(guildID)
	return n, ok && n.OwnerGuildID == guildID
}

// panelFor is the private panel for whoever clicked: the leader's manage
// panel, or the moderation panel.
func (b *Bot) panelFor(i *discordgo.Interaction, p lfg.Post) discordgo.MessageComponent {
	if acc := b.access(i, p); acc == accessMod || acc == accessInCharge {
		return b.modPanel(p, acc, i.GuildID, "", lang(i))
	}
	return b.managePanel(p, lang(i))
}

// welcomeIn reports whether a post may show on a server: its moderators
// didn't take it off, and didn't block its leader.
func (b *Bot) welcomeIn(p lfg.Post, guildID string) bool {
	return !p.HiddenIn(guildID) && !b.store.Blocked(p.LeaderID, guildID)
}

// modPanel is a moderator's private panel for a post, on their server. done
// reports what they just did.
func (b *Bot) modPanel(p lfg.Post, acc access, guildID, done string, l i18n.Lang) discordgo.MessageComponent {
	head := l.T("mod.title") + "\n" + l.T("mod.post", b.runName(p), "**"+escape(p.LeaderName)+"**"+charSuffix(p.LeaderChar),
		escape(orDefault(p.GuildName, p.GuildID)))
	head += "\n" + b.statusLine(p, l)
	comps := []discordgo.MessageComponent{text(head)}
	if done != "" {
		comps = append(comps, text(done))
	}
	comps = append(comps, divider(), text(b.rosterText(p, guildID, false, l)))

	inCharge := acc == accessInCharge
	var buttons []discordgo.MessageComponent
	if p.InGuild(guildID) {
		buttons = append(buttons, discordgo.Button{CustomID: customID(actHide, p.ID), Label: l.T("mod.hide"),
			Style: discordgo.SecondaryButton, Emoji: emoji("❌")})
	}
	if inCharge && p.Active() {
		buttons = append(buttons, discordgo.Button{CustomID: customID(actClose, p.ID), Label: l.T("manage.close"),
			Style: discordgo.SecondaryButton, Emoji: emoji("🔒")})
	}
	if inCharge {
		buttons = append(buttons, discordgo.Button{CustomID: customID(actDelete, p.ID), Label: l.T("manage.delete"),
			Style: discordgo.DangerButton, Emoji: emoji("🗑️")})
	}

	// The menus hold 25 players: the leader, and in a bigger rally the
	// latest to join, the likeliest to be trouble.
	people := slices.Concat(p.Accepted, p.Waiting())
	people = append([]lfg.Player{p.Leader()}, people[max(0, len(people)-maxSelectValues+1):]...)
	comps = append(comps, selectRow(customID(actBlock, p.ID), l.T("mod.block_ph"), len(people), b.playerOptions(p, people, l)))
	if _, owner := b.ownsNetwork(guildID); owner {
		comps = append(comps, selectRow(customID(actNetBlock, p.ID), l.T("mod.net_block_ph"), len(people), b.playerOptions(p, people, l)))
	}
	if inCharge && p.Active() && len(p.Accepted) > 0 {
		comps = append(comps, b.handOverMenu(p, l.T("mod.hand_ph"), l))
	}
	if len(buttons) > 0 {
		comps = append(comps, discordgo.ActionsRow{Components: buttons})
	}
	footer := l.T("mod.footer", b.cmd("lfg-blocks"))
	if inCharge {
		footer = l.T("mod.footer_in_charge", b.cmd("lfg-blocks"))
	}
	comps = append(comps, text(footer))
	return container(colorClosed, comps...)
}

// onHide takes a post off the moderator's server, with its thread there.
func (b *Bot) onHide(i *discordgo.Interaction, p lfg.Post) error {
	l := lang(i)
	if err := b.update(i, notice(colorClosed, l.T("mod.hiding"))); err != nil {
		log.Printf("acknowledge hiding %s: %v", p.ID, err)
	}
	msg := l.T("mod.hidden", b.runName(p))
	if b.hidePost(p.ID, i.GuildID) > 0 {
		msg += l.T("mod.hide_failed")
	}
	return b.editReply(i, notice(colorClosed, msg))
}

// hidePost takes a post off one server for good: its copy and the thread
// under it are deleted, and it isn't sent there again. It returns how many
// of the bot's messages couldn't be deleted; those stay recorded, so the
// cleanup after the post ends removes them.
func (b *Bot) hidePost(postID, guildID string) (failed int) {
	b.store.UpdatePost(postID, func(p *lfg.Post) error {
		if !p.Hide(guildID) {
			return errUnchanged
		}
		return nil
	})
	var here []lfg.MessageRef
	for _, m := range b.store.AllMessages(postID) {
		if m.GuildID == guildID {
			here = append(here, m)
		}
	}
	if failed = b.deleteMessages(postID, here); failed == 0 {
		b.store.RemoveMessagesIn(postID, guildID)
	}
	return failed
}

// onBlock blocks the players picked on a post's moderation panel: on the
// moderator's server, or with network set, in its whole network, which
// only the owner server's moderators can do.
func (b *Bot) onBlock(i *discordgo.Interaction, p lfg.Post, userIDs []string, network bool) error {
	l := lang(i)
	modID, _ := interactionUser(i)
	scope, where := i.GuildID, []string{i.GuildID}
	if network {
		n, owner := b.ownsNetwork(i.GuildID)
		if !owner {
			return b.update(i, b.modPanel(p, b.access(i, p), i.GuildID, "⚠️ "+l.T("err.net.not_owner"), l))
		}
		scope, where = lfg.NetworkScope(n.ID), nil
		for _, m := range n.Members {
			where = append(where, m.GuildID)
		}
	}
	// Answer within Discord's 3 second limit, then fill in the result.
	if err := b.update(i, notice(colorClosed, l.T("mod.blocking"))); err != nil {
		log.Printf("acknowledge blocking on %s: %v", p.ID, err)
	}

	var names []string
	removed := 0
	for _, id := range userIDs {
		pl, ok := personOn(p, id)
		if !ok || id == modID {
			continue
		}
		if err := b.store.Block(lfg.Block{Scope: scope, UserID: id, Name: pl.Name, By: modID}); err != nil {
			log.Printf("block %s in %s: %v", id, scope, err)
			continue
		}
		names = append(names, "**"+escape(pl.Name)+"**")
		removed += b.keepAway(id, i.GuildID, where, network)
	}

	done := l.T("mod.blocked_none")
	if len(names) > 0 {
		key := "mod.blocked"
		if network {
			key = "mod.net_blocked"
		}
		done = l.T(key, strings.Join(names, ", "))
		if removed > 0 {
			done += l.T("mod.blocked_runs", l.N(removed, "n.run"))
		}
	}
	// The post may be gone: the blocked player led it.
	if p, ok := b.store.Post(p.ID); ok && p.InGuild(i.GuildID) {
		return b.editReply(i, b.modPanel(p, b.access(i, p), i.GuildID, "✅ "+done, l))
	}
	return b.editReply(i, notice(colorClosed, "✅ "+done))
}

// personOn finds someone on a post: its leader or a player.
func personOn(p lfg.Post, userID string) (lfg.Player, bool) {
	for _, pl := range p.People() {
		if pl.UserID == userID {
			return pl, true
		}
	}
	return lfg.Player{}, false
}

// keepAway applies a new block to the runs already up and returns how many
// of the player's own runs it took down. Blocked on one server, their runs
// are taken off it. Blocked in a network, their runs there are deleted, and
// they are taken off the runs they joined there.
func (b *Bot) keepAway(userID, guildID string, network []string, whole bool) (removed int) {
	for _, q := range b.store.ActivePosts() {
		switch {
		case !whole && q.LeaderID == userID && q.InGuild(guildID):
			b.hidePost(q.ID, guildID)
			removed++
		case whole && slices.Contains(network, q.GuildID) && q.LeaderID == userID:
			b.removePost(q.ID)
			removed++
		case whole && slices.Contains(network, q.GuildID) && q.Has(userID):
			if p, promoted, err := b.dropPlayer(q.ID, userID); err == nil {
				b.welcome(p, promoted, true)
			}
		}
	}
	return removed
}

// tellLeader lets a post's leader know by DM that moderators of a server
// closed, deleted or handed over their post.
func (b *Bot) tellLeader(p lfg.Post, guildID, key string) {
	l := b.guildLang(p.GuildID)
	b.dm(p.LeaderID, notice(colorClosed, l.T(key, b.runName(p), escape(orDefault(b.guildName(guildID), guildID)))))
}

// /lfg-blocks lists who is blocked on this server, and in its network for
// the owner server, with a menu to lift blocks.
func (b *Bot) cmdBlocks(i *discordgo.Interaction) error {
	l := lang(i)
	if !moderates(i) {
		return b.reply(i, notice(colorClosed, l.T("blocks.not_mod")))
	}
	return b.reply(i, b.blocksPanel(i.GuildID, "", l))
}

// blockScopes are the blocks a server's moderators manage: their own, and
// the network's when the server owns it.
func (b *Bot) blockScopes(guildID string) []string {
	scopes := []string{guildID}
	if n, owner := b.ownsNetwork(guildID); owner {
		scopes = append(scopes, lfg.NetworkScope(n.ID))
	}
	return scopes
}

func (b *Bot) blocksPanel(guildID, done string, l i18n.Lang) discordgo.MessageComponent {
	blocks := b.store.Blocks(b.blockScopes(guildID)...)
	comps := []discordgo.MessageComponent{text(l.T("blocks.title", len(blocks)))}
	if done != "" {
		comps = append(comps, text(done))
	}
	if len(blocks) == 0 {
		return container(colorFull, append(comps, text(l.T("blocks.none")))...)
	}

	var lines strings.Builder
	opts := make([]discordgo.SelectMenuOption, 0, maxSelectValues)
	for n, bl := range blocks {
		where := l.T("blocks.here")
		if bl.Network() {
			where = l.T("blocks.network")
		}
		if n < blocksListed {
			lines.WriteString(l.T("blocks.line", escape(bl.Name), bl.UserID, where, bl.By, bl.At.Unix()))
		}
		if len(opts) < maxSelectValues {
			opts = append(opts, discordgo.SelectMenuOption{
				Label: truncate(bl.Name, 100), Value: bl.Scope + "|" + bl.UserID, Description: truncate(where, 100), Emoji: emoji("⛔"),
			})
		}
	}
	if len(blocks) > blocksListed {
		lines.WriteString(l.T("roster.more", len(blocks)-blocksListed))
	}
	comps = append(comps,
		divider(),
		text(strings.TrimSpace(lines.String())),
		selectRow(blocksPrefix+blocksUnblock, l.T("blocks.unblock_ph"), len(opts), opts),
		text(l.T("blocks.footer")),
	)
	return container(colorFull, comps...)
}

// blocksListed is how many blocks /lfg-blocks writes out, well within the
// length of a message: as many as its menu can lift.
const blocksListed = maxSelectValues

// onBlocks lifts the blocks picked on the /lfg-blocks panel. Only blocks
// the server's moderators manage can be lifted from it.
func (b *Bot) onBlocks(i *discordgo.Interaction, field string, values []string) error {
	l := lang(i)
	if !moderates(i) {
		return b.reply(i, notice(colorClosed, l.T("blocks.not_mod")))
	}
	if field != blocksUnblock {
		return b.update(i, b.blocksPanel(i.GuildID, "", l))
	}
	scopes := b.blockScopes(i.GuildID)
	lifted := 0
	for _, v := range values {
		scope, userID, _ := strings.Cut(v, "|")
		if slices.Contains(scopes, scope) && b.store.Unblock(scope, userID) {
			lifted++
		}
	}
	return b.update(i, b.blocksPanel(i.GuildID, l.T("blocks.lifted", lifted), l))
}
