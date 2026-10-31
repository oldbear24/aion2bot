package bot

import (
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
)

// /help answers privately in the user's language; /help-public posts the
// guide in the channel in the server's language. Only server admins asking
// privately see the admin section.
func (b *Bot) cmdHelp(i *discordgo.Interaction, public bool) error {
	l := lang(i)
	flags := discordgo.MessageFlagsIsComponentsV2
	if public {
		l = b.guildLang(i.GuildID)
	} else {
		flags |= discordgo.MessageFlagsEphemeral
	}
	return b.respond(i, discordgo.InteractionResponseChannelMessageWithSource, v2Message{
		Components:      []discordgo.MessageComponent{b.helpPanel(l, !public && managesServer(i))},
		Flags:           flags,
		AllowedMentions: noMentions,
	})
}

// managesServer reports whether the user may run the admin commands, which
// need Manage Server. In DMs there is no server, so it is false.
func managesServer(i *discordgo.Interaction) bool {
	return i.Member != nil &&
		i.Member.Permissions&(discordgo.PermissionManageGuild|discordgo.PermissionAdministrator) != 0
}

func (b *Bot) helpPanel(l i18n.Lang, admin bool) discordgo.MessageComponent {
	var roles []string
	for _, r := range b.game.Roles {
		roles = append(roles, strings.TrimSpace(r.Emoji+" "+r.Name))
	}
	parts := []discordgo.MessageComponent{
		text(l.T("help.title")),
		text(l.T("help.find", b.cmd("lfg-find"), b.cmd("lfg-notify"), l.N(b.game.ReminderMinutes, "n.minutes"), b.cmd("profile"))),
		divider(),
		text(l.T("help.post", b.cmd("lfg"), strings.Join(roles, " · "),
			b.game.MaxRunsPerLeader, l.N(b.game.PostTTLHours, "n.hours"), b.cmd("lfg-stats"))),
	}
	if admin {
		parts = append(parts, divider(),
			text(l.T("help.admin", b.cmd("lfg-setup"), b.cmd("lfg-check"), b.cmd("lfg-network create"),
				b.cmd("lfg-network join"), b.cmd("lfg-network info"), b.cmd("lfg-disable"), b.cmd("help-public"))))
	}
	parts = append(parts, text(l.T("help.footer", b.cmd("help"))))
	return container(colorFull, parts...)
}

// cmd is a clickable mention of a slash command (or subcommand, such as
// "lfg-network join"). Before the commands are registered it falls back to
// plain text.
func (b *Bot) cmd(name string) string {
	b.cmdMu.RLock()
	id, ok := b.commandIDs[strings.Fields(name)[0]]
	b.cmdMu.RUnlock()
	if !ok {
		return "`/" + name + "`"
	}
	return "</" + name + ":" + id + ">"
}
