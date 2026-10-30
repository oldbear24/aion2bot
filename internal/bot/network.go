package bot

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

func networkCommand(manageServer *int64, guildOnly *[]discordgo.InteractionContextType) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:                     "lfg-network",
		Description:              cmdText("cmd.network"),
		DescriptionLocalizations: cmdTexts("cmd.network"),
		DefaultMemberPermissions: manageServer,
		Contexts:                 guildOnly,
		Options: []*discordgo.ApplicationCommandOption{
			subcommand("create", "cmd.network.create", &discordgo.ApplicationCommandOption{
				Type: discordgo.ApplicationCommandOptionString, Name: "name", Required: true, MaxLength: 50,
				Description: cmdText("cmd.network.create.name"), DescriptionLocalizations: *cmdTexts("cmd.network.create.name"),
			}),
			subcommand("join", "cmd.network.join", &discordgo.ApplicationCommandOption{
				Type: discordgo.ApplicationCommandOptionString, Name: "code", Required: true, MaxLength: 20,
				Description: cmdText("cmd.network.join.code"), DescriptionLocalizations: *cmdTexts("cmd.network.join.code"),
			}),
			subcommand("info", "cmd.network.info", nil),
			subcommand("leave", "cmd.network.leave", nil),
			subcommand("remove", "cmd.network.remove", &discordgo.ApplicationCommandOption{
				Type: discordgo.ApplicationCommandOptionString, Name: "server", Required: true, Autocomplete: true,
				Description: cmdText("cmd.network.remove.server"), DescriptionLocalizations: *cmdTexts("cmd.network.remove.server"),
			}),
			subcommand("new-code", "cmd.network.new_code", nil),
		},
	}
}

func subcommand(name, key string, option *discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
	sub := &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionSubCommand, Name: name,
		Description: cmdText(key), DescriptionLocalizations: *cmdTexts(key),
	}
	if option != nil {
		sub.Options = []*discordgo.ApplicationCommandOption{option}
	}
	return sub
}

func (b *Bot) cmdNetwork(i *discordgo.Interaction) error {
	l := lang(i)
	sub := i.ApplicationCommandData().Options[0]
	opts := map[string]string{}
	for _, o := range sub.Options {
		opts[o.Name] = strings.TrimSpace(fmt.Sprint(o.Value))
	}
	guildName := b.guildName(i.GuildID)

	switch sub.Name {
	case "create":
		if opts["name"] == "" {
			return b.reply(i, notice(colorClosed, l.T("net.need_name")))
		}
		n, err := b.store.CreateNetwork(opts["name"], i.GuildID, guildName)
		if err != nil {
			return b.networkFailed(i, err)
		}
		return b.reply(i, b.networkPanel(n, i.GuildID, l.T("net.created"), l))

	case "join":
		code := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(opts["code"]))
		n, err := b.store.JoinNetwork(code, i.GuildID, guildName)
		if err != nil {
			return b.networkFailed(i, err)
		}
		err = b.reply(i, b.networkPanel(n, i.GuildID, l.T("net.joined"), l))
		b.shareLinkedPosts(i.GuildID)
		return err

	case "info":
		n, ok := b.store.NetworkOf(i.GuildID)
		if !ok {
			return b.reply(i, notice(colorClosed, l.T("net.none")))
		}
		return b.reply(i, b.networkPanel(n, i.GuildID, "", l))

	case "leave":
		n, err := b.store.LeaveNetwork(i.GuildID)
		if err != nil {
			return b.networkFailed(i, err)
		}
		return b.reply(i, notice(colorClosed, l.T("net.left", escape(n.Name))))

	case "remove":
		n, err := b.store.RemoveFromNetwork(i.GuildID, opts["server"])
		if err != nil {
			return b.networkFailed(i, err)
		}
		return b.reply(i, b.networkPanel(n, i.GuildID, l.T("net.removed"), l))

	case "new-code":
		n, err := b.store.NewNetworkCode(i.GuildID)
		if err != nil {
			return b.networkFailed(i, err)
		}
		return b.reply(i, b.networkPanel(n, i.GuildID, l.T("net.new_code"), l))
	}
	return nil
}

// onNetworkAutocomplete suggests the servers the owner can remove.
func (b *Bot) onNetworkAutocomplete(i *discordgo.Interaction) error {
	var typed string
	for _, sub := range i.ApplicationCommandData().Options {
		for _, o := range sub.Options {
			if o.Focused {
				typed = strings.ToLower(fmt.Sprint(o.Value))
			}
		}
	}
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	if n, ok := b.store.NetworkOf(i.GuildID); ok {
		for _, m := range n.Members {
			if m.GuildID == i.GuildID || !strings.Contains(strings.ToLower(m.GuildName), typed) || len(choices) == 25 {
				continue
			}
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name: truncate(orDefault(m.GuildName, m.GuildID), 100), Value: m.GuildID,
			})
		}
	}
	return b.respond(i, discordgo.InteractionApplicationCommandAutocompleteResult,
		struct {
			Choices []*discordgo.ApplicationCommandOptionChoice `json:"choices"`
		}{choices})
}

func (b *Bot) networkPanel(n lfg.Network, viewerGuildID, headline string, l i18n.Lang) discordgo.MessageComponent {
	configured := map[string]bool{}
	for _, g := range b.store.Guilds() {
		configured[g.GuildID] = true
	}
	var list strings.Builder
	for _, m := range n.Members {
		name := escape(orDefault(m.GuildName, m.GuildID))
		if m.GuildID == n.OwnerGuildID {
			list.WriteString("\n👑 " + name + l.T("net.owner"))
		} else {
			list.WriteString("\n• " + name)
		}
		if m.GuildID == viewerGuildID {
			list.WriteString(l.T("net.this"))
		}
		if !configured[m.GuildID] {
			list.WriteString(l.T("net.no_channel"))
		}
	}

	head := "### 🔗 " + escape(n.Name)
	if headline != "" {
		head += "\n" + headline
	}
	return container(colorFull,
		text(head),
		text(l.T("net.code", n.Code)),
		divider(),
		text(l.T("net.servers", len(n.Members), list.String())),
		text(l.T("net.footer")),
	)
}

func (b *Bot) networkFailed(i *discordgo.Interaction, err error) error {
	l := lang(i)
	key := ""
	switch {
	case errors.Is(err, lfg.ErrInNetwork):
		key = "err.net.in_network"
	case errors.Is(err, lfg.ErrBadCode):
		key = "err.net.bad_code"
	case errors.Is(err, lfg.ErrNoNetwork):
		key = "err.net.no_network"
	case errors.Is(err, lfg.ErrNotOwner):
		key = "err.net.not_owner"
	case errors.Is(err, lfg.ErrRemoveOwner):
		key = "err.net.remove_owner"
	case errors.Is(err, lfg.ErrNotMember):
		key = "err.net.not_member"
	default:
		b.reply(i, notice(colorClosed, errorText(err, l)))
		return err
	}
	return b.reply(i, notice(colorClosed, l.T(key)))
}

// shareLinkedPosts sends the open posts of a server's network to every
// linked channel that doesn't have them yet, in both directions.
func (b *Bot) shareLinkedPosts(guildID string) {
	linked := b.store.Linked(guildID)
	for _, p := range b.store.ActivePosts() {
		if slices.Contains(linked, p.GuildID) {
			b.broadcast(p, b.store.Targets(p.GuildID), false)
		}
	}
}
