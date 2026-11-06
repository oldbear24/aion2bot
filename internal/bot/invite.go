package bot

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// The leader invites players to an open post from Manage, with Discord's
// member picker. Each is pinged in the thread under the post on the server
// they were invited on, or gets a DM, with a Join button. Once they join,
// they are in the party straight away while it has room, whatever role and
// class they bring and whatever the post asks for gear score; once it is
// full, they join as reserves. The leader can withdraw invitations until
// then, and an invited player finds theirs in /lfg-mine.

const (
	// inviteBatch is how many players the leader picks at once.
	inviteBatch = 10
	// inviteListBudget keeps the open invitations on the manage panel short,
	// as the panel's texts share Discord's 4000 characters with the party.
	inviteListBudget = 400
)

// inviteControls are the manage panel's member picker, and the open
// invitations with a menu to withdraw them.
func (b *Bot) inviteControls(p lfg.Post, l i18n.Lang) []discordgo.MessageComponent {
	one, room := 1, lfg.MaxInvites-len(p.Invites)
	picker := discordgo.SelectMenu{MenuType: discordgo.UserSelectMenu, CustomID: customID(actInvite, p.ID),
		Placeholder: l.T("invite.pick_ph"), MinValues: &one, MaxValues: max(1, min(inviteBatch, room)), Disabled: room <= 0}
	comps := []discordgo.MessageComponent{
		text(l.T("invite.label")),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{picker}},
	}
	if len(p.Invites) == 0 {
		return comps
	}
	names := make([]string, 0, len(p.Invites))
	opts := make([]discordgo.SelectMenuOption, 0, len(p.Invites))
	left := inviteListBudget
	for n, inv := range p.Invites {
		opts = append(opts, discordgo.SelectMenuOption{Label: truncate(inv.Name, 100), Value: inv.UserID, Emoji: emoji("📨")})
		if left <= 0 {
			continue
		}
		name := "**" + escape(truncate(inv.Name, 32)) + "**"
		if left -= utf8.RuneCountInString(name) + 2; left < 0 {
			names = append(names, l.T("invite.more", len(p.Invites)-n))
			continue
		}
		names = append(names, name)
	}
	return append(comps, text(l.T("invite.open", len(p.Invites), strings.Join(names, ", "))),
		selectRow(customID(actUninvite, p.ID), l.T("invite.withdraw_ph"), len(opts), opts))
}

// onInvite invites the players the leader picked: the picker offers the
// members of the server it is on. Bots, players blocked there or on the
// post's server, and those already on the post or invited aren't. The panel
// says who was, and they hear about it.
func (b *Bot) onInvite(i *discordgo.Interaction, before lfg.Post, userIDs []string) error {
	l := lang(i)
	if !before.Active() {
		return b.update(i, b.managePanel(before, l))
	}
	resolved := i.MessageComponentData().Resolved
	skipped := map[string][]string{}
	skip := func(why, name string) { skipped[why] = append(skipped[why], name) }
	var asked []lfg.Invite
	for _, id := range userIDs {
		name := id
		u := resolved.Users[id]
		if u != nil {
			name = u.DisplayName()
		}
		if m := resolved.Members[id]; m != nil && m.Nick != "" {
			name = m.Nick
		}
		switch {
		case u != nil && u.Bot:
			skip("invite.why_bot", name)
		case b.blockedFrom(before, id, i.GuildID):
			skip("invite.why_blocked", name)
		default:
			asked = append(asked, lfg.Invite{UserID: id, Name: name, GuildID: i.GuildID})
		}
	}

	var sent []lfg.Invite
	refused := map[string][]string{}
	p, err := b.store.UpdatePost(before.ID, func(p *lfg.Post) error {
		sent, refused = nil, map[string][]string{}
		for _, inv := range asked {
			switch err := p.Invite(inv); {
			case err == nil:
				sent = append(sent, inv)
			case errors.Is(err, lfg.ErrNotOpen):
				return err
			case errors.Is(err, lfg.ErrInvitedAlready):
				refused["invite.why_invited"] = append(refused["invite.why_invited"], inv.Name)
			case errors.Is(err, lfg.ErrTooManyInvites):
				refused["invite.why_full"] = append(refused["invite.why_full"], inv.Name)
			default:
				refused["invite.why_on_post"] = append(refused["invite.why_on_post"], inv.Name)
			}
		}
		if len(sent) == 0 {
			return errUnchanged
		}
		return nil
	})
	switch {
	case errors.Is(err, lfg.ErrNotFound):
		return b.update(i, notice(colorClosed, errorText(err, l)))
	case err != nil && !errors.Is(err, errUnchanged):
		// The post ended meanwhile; show it as it is.
		return b.update(i, b.managePanel(p, l))
	}
	for why, names := range refused {
		skipped[why] = append(skipped[why], names...)
	}

	err = b.update(i, b.managePanelSaying(p, inviteSummary(sent, skipped, l), l))
	if len(sent) > 0 {
		b.sendInvites(p, sent)
	}
	return err
}

// inviteSummary says whom the leader invited, and whom not and why.
func inviteSummary(sent []lfg.Invite, skipped map[string][]string, l i18n.Lang) string {
	var lines []string
	if len(sent) > 0 {
		names := make([]string, 0, len(sent))
		for _, inv := range sent {
			names = append(names, "**"+escape(truncate(inv.Name, 32))+"**")
		}
		lines = append(lines, l.T("invite.sent", strings.Join(names, ", ")))
	}
	var why []string
	for _, key := range []string{"invite.why_on_post", "invite.why_invited", "invite.why_blocked", "invite.why_bot", "invite.why_full"} {
		if len(skipped[key]) == 0 {
			continue
		}
		names := make([]string, 0, len(skipped[key]))
		for _, name := range skipped[key] {
			names = append(names, "**"+escape(truncate(name, 32))+"**")
		}
		why = append(why, strings.Join(names, ", ")+" ("+l.T(key)+")")
	}
	if len(why) > 0 {
		lines = append(lines, l.T("invite.not_invited", strings.Join(why, " · ")))
	}
	return strings.Join(lines, "\n")
}

// sendInvites pings the invited players in the thread under the post on
// the server they were invited on, and DMs those it doesn't reach. The note
// shows in the threads on every server.
func (b *Bot) sendInvites(p lfg.Post, invites []lfg.Invite) {
	now := time.Now()
	pings := map[string][]string{}
	for _, inv := range invites {
		// The picker offers the members of the server it is on.
		if inv.GuildID != "" {
			b.members.set(inv.GuildID, inv.UserID, true, now)
		}
		pings[inv.GuildID] = append(pings[inv.GuildID], inv.UserID)
	}
	b.learnMembers(p, p.GuildIDs())
	reached := b.announce(p, pings, true, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.inviteNote(p, invites, guildID, l)
	})
	for _, inv := range invites {
		if !reached[inv.GuildID] {
			b.dm(inv.UserID, b.inviteDM(p, b.guildLang(inv.GuildID)))
		}
	}
}

// onUninvite withdraws the invitations the leader picked.
func (b *Bot) onUninvite(i *discordgo.Interaction, p lfg.Post, userIDs []string) error {
	return b.leaderUpdate(i, p.ID, func(p *lfg.Post) error {
		withdrawn := false
		for _, id := range userIDs {
			withdrawn = p.Uninvite(id) || withdrawn
		}
		if !withdrawn {
			return errUnchanged
		}
		return nil
	})
}

// joinLooking is whom a player may join a post as: what it is looking for,
// or anything for a player the leader invited.
func joinLooking(p lfg.Post, userID string) []string {
	if p.IsInvited(userID) {
		return []string{config.AnyID}
	}
	return p.LookingFor
}

// inviteNote tells the threads the leader invited players, who are pinged
// with it on the server they were invited on, with the button to join.
func (b *Bot) inviteNote(p lfg.Post, invites []lfg.Invite, guildID string, l i18n.Lang) discordgo.MessageComponent {
	leader := who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	names := make([]string, 0, len(invites))
	for _, inv := range invites {
		names = append(names, who(inv.UserID, inv.Name, b.mentionable(inv.UserID, inv.GuildID, guildID)))
	}
	return container(colorOpen,
		text(l.T("thread.invited_title")+"\n"+l.T("thread.invited", leader, strings.Join(names, ", "), b.runName(p))+"\n"+
			l.T("card.start")+postStart(p, l)),
		divider(),
		discordgo.Section{Components: []discordgo.MessageComponent{text(l.T("thread.invited_hint"))}, Accessory: joinButton(p.ID, l)},
	)
}

// inviteDM is the invitation for a player the thread didn't reach.
func (b *Bot) inviteDM(p lfg.Post, l i18n.Lang) discordgo.MessageComponent {
	return container(colorOpen,
		text(l.T("dm.invited", escape(p.LeaderName), p.LeaderID, b.runName(p), postStart(p, l))+leaderCharLine(p, l)),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{joinButton(p.ID, l)}},
	)
}

// joinButton opens the join form, like the card's.
func joinButton(postID string, l i18n.Lang) discordgo.Button {
	return discordgo.Button{CustomID: customID(actJoin, postID), Label: l.T("card.join"), Style: discordgo.SuccessButton, Emoji: emoji("✋")}
}
