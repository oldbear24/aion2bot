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
		text(l.T("help.find", b.cmd("lfg-find"), b.cmd("lfg-notify"), l.N(b.game.ReminderMinutes, "n.minutes"), b.cmd("lfg-mine"),
			b.cmd("profile"))),
		divider(),
		text(l.T("help.post", b.cmd("lfg"), strings.Join(roles, " · "),
			b.game.MaxRunsPerLeader, l.N(b.game.PostTTLHours, "n.hours"), b.cmd("lfg-stats"))),
	}
	if len(b.game.Abyss) > 0 {
		names := make([]string, 0, len(b.game.Abyss))
		for _, d := range b.game.Abyss {
			names = append(names, d.Name)
		}
		parts = append(parts, divider(), text(l.T("help.abyss", b.cmd("lfg-abyss"), strings.Join(names, ", "))))
	}
	if admin {
		parts = append(parts, divider(),
			text(l.T("help.admin", b.cmd("lfg-setup"), b.cmd("lfg-check"), b.cmd("lfg-network create"),
				b.cmd("lfg-network join"), b.cmd("lfg-network info"), b.cmd("lfg-disable"), b.cmd("help-public"),
				b.cmd("lfg-blocks"))))
	}
	parts = append(parts, text(l.T("help.footer", b.cmd("help"), b.cmd("legend"))))
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

// /legend explains the icons on posts, privately in the user's language.
func (b *Bot) cmdLegend(i *discordgo.Interaction) error { return b.reply(i, b.legendPanel(lang(i))) }

// legendPanel lists the roles, every class with the roles it fills, and
// the other marks a post uses.
func (b *Bot) legendPanel(l i18n.Lang) discordgo.MessageComponent {
	role := func(id string) string {
		r, _ := b.game.Role(id)
		return strings.TrimSpace(r.Emoji + " " + r.Name)
	}
	roles := make([]string, 0, len(b.game.Roles)+1)
	for _, r := range b.game.Roles {
		roles = append(roles, role(r.ID))
	}
	roles = append(roles, anyEmoji+" "+l.T("any.name"))

	var classes strings.Builder
	for _, c := range b.game.Classes {
		classes.WriteString("\n" + strings.TrimSpace(c.Emoji+" **"+c.Name+"**"))
		fills := make([]string, 0, len(c.Roles))
		for _, id := range c.Roles {
			fills = append(fills, role(id))
		}
		if len(fills) > 0 {
			classes.WriteString(" — " + strings.Join(fills, " · "))
		}
	}
	for _, c := range b.game.Classes {
		for _, id := range c.BackupRoles {
			if instead, ok := b.game.Backup(c.ID, id); ok {
				classes.WriteString(l.T("legend.backup", c.Name, role(id), role(instead)))
			}
		}
	}

	comps := []discordgo.MessageComponent{
		text(l.T("legend.title")),
		text(l.T("legend.roles") + strings.Join(roles, " · ")),
		text(l.T("legend.classes") + classes.String()),
	}
	if len(b.game.Factions) > 0 {
		factions := make([]string, 0, len(b.game.Factions))
		for _, f := range b.game.Factions {
			factions = append(factions, b.factionTag(f.ID))
		}
		comps = append(comps, text(l.T("legend.factions")+strings.Join(factions, " · ")))
	}
	return container(colorFull, append(comps, divider(), text(l.T("legend.post", b.game.LeaderEmoji)))...)
}
