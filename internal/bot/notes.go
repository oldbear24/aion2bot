package bot

import (
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// The notes the bot posts in the threads under a post are cards like the
// post itself, in the colour of what happened: a heading and who it is
// for, a divider, then the details and a small footer. Each is rendered
// for one server, in its language, so the people on it are linked with
// mentions where Discord can resolve them; only the users the caller lets
// through allowed_mentions are pinged.

// joinNote tells the leader that a player wants to join, or signed up as a
// reserve, with a Manage button to accept them.
func (b *Bot) joinNote(p lfg.Post, pl lfg.Player, reserve bool, guildID string, l i18n.Lang) discordgo.MessageComponent {
	color, title, intro, hint := colorOpen, "thread.join_title", "thread.join", "thread.join_hint"
	if reserve {
		color, title, intro, hint = colorFull, "thread.reserve_title", "thread.reserve", "thread.reserve_hint"
	}
	leader := who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	return container(color,
		text(l.T(title)+"\n"+l.T(intro, leader, b.runName(p))+"\n"+b.memberLine(p, pl, guildID, false, l)),
		divider(),
		discordgo.Section{Components: []discordgo.MessageComponent{text(l.T(hint))}, Accessory: manageButton(p.ID, true, l)},
		text(l.T("thread.join_footer")),
	)
}

// welcomeNote tells a player they are in the party: accepted by the
// leader, or moved up from the reserves.
func (b *Bot) welcomeNote(p lfg.Post, pl lfg.Player, promoted bool, guildID string, l i18n.Lang) discordgo.MessageComponent {
	title, intro := "thread.accepted_title", "thread.accepted"
	if promoted {
		title, intro = "thread.promoted_title", "thread.promoted"
	}
	player := who(pl.UserID, pl.Name, b.mentionable(pl.UserID, pl.GuildID, guildID))
	leader := who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	details := []string{
		l.T("thread.playing", b.bringing(p, pl)),
		l.T("card.start") + startText(p.StartAt, p.Start, l),
	}
	if p.LeaderChar != "" {
		details = append(details, l.T("thread.leader_char", charTag(p.LeaderChar)))
	}
	return container(colorOpen,
		text(l.T(title)+"\n"+l.T(intro, player, leader, b.runName(p))),
		divider(),
		text(strings.Join(details, "\n")+"\n"+l.T("thread.invite_footer")),
	)
}

// reminderNote tells the party the run starts soon, and lists who is in
// it, by role. The roster shows the leader's character name, so it needs
// no line of its own.
func (b *Bot) reminderNote(p lfg.Post, guildID string, l i18n.Lang) discordgo.MessageComponent {
	at := p.StartAt.Unix()
	party := b.partyByRole(p)
	room := rosterBudget
	roster := l.T("roster.in_party", len(party)) +
		playerList(party, func(pl lfg.Player) string { return b.memberLine(p, pl, guildID, false, l) }, &room, l)
	return container(colorFull,
		text(l.T("thread.reminder_title", at)+"\n"+l.T("thread.reminder", b.runName(p), at)),
		divider(),
		text(roster),
	)
}

// changesNote tells the threads what the leader changed on the post. The
// caller checks that something did.
func (b *Bot) changesNote(before, after lfg.Post, guildID string, l i18n.Lang) discordgo.MessageComponent {
	var lines []string
	if !before.StartAt.Equal(after.StartAt) {
		lines = append(lines, l.T("card.start")+startText(after.StartAt, "", l))
	}
	if before.MinGS != after.MinGS {
		gs := l.T("thread.gs_none")
		if after.MinGS > 0 {
			gs = l.GS(after.MinGS) + "+"
		}
		lines = append(lines, l.T("thread.gs", gs))
	}
	if before.Note != after.Note {
		lines = append(lines, l.T("thread.note", orDefault(escape(after.Note), l.T("draft.no_note"))))
	}
	leader := who(after.LeaderID, after.LeaderName, b.mentionable(after.LeaderID, after.GuildID, guildID))
	return container(statusColor(after.Status),
		text(l.T("thread.edited_title")+"\n"+l.T("thread.edited", leader)),
		divider(),
		text(strings.Join(lines, "\n")),
	)
}

// moveNote tells the threads the leader took the post to another dungeon
// or mode, and how the party stands there.
func (b *Bot) moveNote(before, after lfg.Post, guildID string, l i18n.Lang) discordgo.MessageComponent {
	leader := who(after.LeaderID, after.LeaderName, b.mentionable(after.LeaderID, after.GuildID, guildID))
	return container(statusColor(after.Status),
		text(l.T("thread.moved_title")+"\n"+l.T("thread.moved", leader, b.runName(before), b.runName(after))),
		divider(),
		text(b.statusLine(after, l)),
	)
}

// reopenNote tells the threads the leader put an ended post back up.
func (b *Bot) reopenNote(p lfg.Post, guildID string, l i18n.Lang) discordgo.MessageComponent {
	leader := who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	return container(statusColor(p.Status),
		text(l.T("thread.reopened_title")+"\n"+l.T("thread.reopened", leader)),
		divider(),
		text(b.statusLine(p, l)),
	)
}

// manageButton opens the leader's panel. It is grey where everyone sees
// it, on the card and in the threads, and green where only the leader
// does.
func manageButton(postID string, public bool, l i18n.Lang) discordgo.Button {
	style := discordgo.SuccessButton
	if public {
		style = discordgo.SecondaryButton
	}
	return discordgo.Button{CustomID: customID(actManage, postID), Label: l.T("card.manage"), Style: style, Emoji: emoji("⚙️")}
}
