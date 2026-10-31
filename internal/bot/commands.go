package bot

import (
	"strconv"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
)

// commandList is every slash command, with descriptions in English
// (Discord's default) and Czech.
func commandList(game *config.Game) []*discordgo.ApplicationCommand {
	manageServer := int64(discordgo.PermissionManageGuild)
	manageMessages := int64(discordgo.PermissionManageMessages)
	guildOnly := &[]discordgo.InteractionContextType{discordgo.InteractionContextGuild}
	guildAndDM := &[]discordgo.InteractionContextType{discordgo.InteractionContextGuild, discordgo.InteractionContextBotDM}
	lfgChannels := []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}

	languages := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(i18n.Langs))
	for _, l := range i18n.Langs {
		languages = append(languages, &discordgo.ApplicationCommandOptionChoice{Name: l.Name(), Value: string(l)})
	}
	dungeons := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(game.Dungeons))
	for _, d := range game.Dungeons {
		dungeons = append(dungeons, &discordgo.ApplicationCommandOptionChoice{Name: truncate(d.Name, 100), Value: d.ID})
	}
	periods := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(statsPeriods))
	for _, days := range statsPeriods {
		key := "stats.period." + strconv.Itoa(days)
		periods = append(periods, &discordgo.ApplicationCommandOptionChoice{
			Name: cmdText(key), NameLocalizations: *cmdTexts(key), Value: days,
		})
	}

	return []*discordgo.ApplicationCommand{
		command("lfg", "cmd.lfg", nil, guildOnly),
		command("lfg-find", "cmd.find", nil, guildOnly,
			option(discordgo.ApplicationCommandOptionString, "dungeon", "cmd.find.dungeon", false, func(o *discordgo.ApplicationCommandOption) {
				o.Choices = dungeons
			}),
		),
		command("lfg-notify", "cmd.notify", nil, guildOnly),
		command("lfg-stats", "cmd.stats", nil, guildOnly,
			option(discordgo.ApplicationCommandOptionInteger, "days", "cmd.stats.days", false, func(o *discordgo.ApplicationCommandOption) {
				o.Choices = periods
			}),
		),
		command("profile", "cmd.profile", nil, guildAndDM),
		command("help", "cmd.help", nil, guildAndDM),
		// Limited to moderators by default so the channel isn't spammed;
		// admins can open it up under Server Settings → Integrations.
		command("help-public", "cmd.help_public", &manageMessages, guildOnly),
		command("lfg-setup", "cmd.setup", &manageServer, guildOnly,
			option(discordgo.ApplicationCommandOptionChannel, "channel", "cmd.setup.channel", true, func(o *discordgo.ApplicationCommandOption) {
				o.ChannelTypes = lfgChannels
			}),
			option(discordgo.ApplicationCommandOptionRole, "ping_role", "cmd.setup.ping_role", false, nil),
			option(discordgo.ApplicationCommandOptionString, "language", "cmd.setup.language", false, func(o *discordgo.ApplicationCommandOption) {
				o.Choices = languages
			}),
		),
		command("lfg-check", "cmd.check", &manageServer, guildOnly,
			option(discordgo.ApplicationCommandOptionChannel, "channel", "cmd.check.channel", false, func(o *discordgo.ApplicationCommandOption) {
				o.ChannelTypes = lfgChannels
			}),
		),
		command("lfg-disable", "cmd.disable", &manageServer, guildOnly),
		networkCommand(&manageServer, guildOnly),
	}
}

func command(name, key string, perms *int64, contexts *[]discordgo.InteractionContextType,
	options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:                     name,
		Description:              cmdText(key),
		DescriptionLocalizations: cmdTexts(key),
		DefaultMemberPermissions: perms,
		Contexts:                 contexts,
		Options:                  options,
	}
}

func option(typ discordgo.ApplicationCommandOptionType, name, key string, required bool,
	extra func(o *discordgo.ApplicationCommandOption)) *discordgo.ApplicationCommandOption {
	o := &discordgo.ApplicationCommandOption{
		Type: typ, Name: name, Required: required,
		Description: cmdText(key), DescriptionLocalizations: *cmdTexts(key),
	}
	if extra != nil {
		extra(o)
	}
	return o
}

// cmdText is a command description in English, Discord's default.
func cmdText(key string) string { return i18n.English.T(key) }

// cmdTexts is the same description in the other languages the bot speaks;
// Discord shows it to users whose client uses that language.
func cmdTexts(key string) *map[discordgo.Locale]string {
	return &map[discordgo.Locale]string{discordgo.Czech: i18n.Czech.T(key)}
}
