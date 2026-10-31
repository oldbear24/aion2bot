package bot

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const (
	colorOpen   = 0x2ECC71
	colorFull   = 0x3498DB
	colorClosed = 0x747F8D

	// Keeps the card well under the 4000 character limit for text displays.
	maxListed = 20
)

// Post components use "lfg:<action>:<postID>".
const (
	actJoin      = "join"
	actJoinForm  = "joinform"
	actLeave     = "leave"
	actManage    = "manage"
	actAccept    = "accept"
	actKick      = "kick"
	actMore      = "more"
	actLess      = "less"
	actClose     = "close"
	actDelete    = "delete"
	actDeleteYes = "deleteyes"
	actPanel     = "panel"
)

// The /lfg panel uses "lfgd:<field>". It is ephemeral, so the draft is
// found by the user who clicks.
const (
	draftPrefix     = "lfgd:"
	fieldDungeon    = "dungeon"
	fieldMode       = "mode"
	fieldLookingFor = "lookingfor"
	fieldMissing    = "missing"
	fieldDetails    = "details"
	fieldPost       = "post"
	fieldCancel     = "cancel"
)

func customID(action, postID string) string { return "lfg:" + action + ":" + postID }

func draftID(field string) string { return draftPrefix + field }

func parseCustomID(id string) (action, postID string, ok bool) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || parts[0] != "lfg" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// lang is the language of the user who clicked or typed a command.
func lang(i *discordgo.Interaction) i18n.Lang { return i18n.Parse(string(i.Locale)) }

// guildLang is the language a server chose for its LFG posts.
func (b *Bot) guildLang(guildID string) i18n.Lang {
	if cfg, ok := b.store.Guild(guildID); ok {
		return i18n.Parse(cfg.Lang)
	}
	return i18n.English
}

// postCard is the public post shown in one server's LFG channel, in that
// server's language. Each server gets its own render so its members can be
// linked with mentions. Editing a message never notifies the users it
// mentions.
func (b *Bot) postCard(p lfg.Post, guildID, pingRoleID string, l i18n.Lang) []discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)

	var status string
	switch p.Status {
	case lfg.StatusOpen:
		status = l.T("card.looking", p.Missing, partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
	case lfg.StatusFull:
		status = l.T("card.full") + partyFill(dungeon, p)
	case lfg.StatusClosed:
		status = l.T("card.closed")
	case lfg.StatusExpired:
		status = l.T("card.expired")
	}
	header := "## " + strings.TrimSpace(dungeon.Emoji+" "+dungeon.Name)
	if line := difficultyLine(dungeon, p.ModeID, p.MinGS, l); line != "" {
		header += "\n" + line
	}
	header += "\n" + status

	info := l.T("card.leader") + who(p.LeaderID, p.LeaderName, b.mentionable(p.LeaderID, p.GuildID, guildID))
	if p.GuildName != "" && p.GuildID != guildID {
		info += " · *" + escape(truncate(p.GuildName, 40)) + "*"
	}
	info += "\n" + l.T("card.start") + startText(p.StartAt, p.Start, l)

	var body []discordgo.MessageComponent
	if dungeon.Image != "" {
		body = append(body, discordgo.Section{
			Components: []discordgo.MessageComponent{text(header), text(info)},
			Accessory:  discordgo.Thumbnail{Media: discordgo.UnfurledMediaItem{URL: dungeon.Image}},
		})
	} else {
		body = append(body, text(header), text(info))
	}
	if p.Note != "" {
		body = append(body, text(quote(escape(p.Note))))
	}
	body = append(body, divider(), text(b.rosterText(p, guildID, false, l)))

	body = append(body, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{CustomID: customID(actJoin, p.ID), Label: l.T("card.join"), Style: discordgo.SuccessButton,
			Emoji: emoji("✋"), Disabled: p.Status != lfg.StatusOpen},
		discordgo.Button{CustomID: customID(actLeave, p.ID), Label: l.T("card.leave"), Style: discordgo.SecondaryButton,
			Disabled: !p.Active()},
		discordgo.Button{CustomID: customID(actManage, p.ID), Label: l.T("card.manage"), Style: discordgo.SecondaryButton,
			Emoji: emoji("⚙️"), Disabled: !p.Active()},
	}})

	footer := l.T("card.footer_open", p.ID, p.ExpiresAt.Unix())
	if !p.Active() {
		footer = l.T("card.footer_ended", p.ID, p.ClosedAt.Unix(), p.ClosedAt.Add(keepEndedPosts).Unix())
	}
	body = append(body, text(footer))

	var out []discordgo.MessageComponent
	if pingRoleID != "" {
		out = append(out, text("<@&"+pingRoleID+">"))
	}
	return append(out, container(statusColor(p.Status), body...))
}

// difficultyLine is "**Conquest [Hard]** · GS 4,500+", or less when the
// post has no mode or gear score.
func difficultyLine(d config.Dungeon, modeID string, minGS int, l i18n.Lang) string {
	var parts []string
	if m, ok := d.Mode(modeID); ok {
		parts = append(parts, "**"+m.Name+"**")
	}
	if minGS > 0 {
		parts = append(parts, "GS "+l.GS(minGS)+"+")
	}
	return strings.Join(parts, " · ")
}

// who links a user with a mention where Discord can resolve it, on a
// server they're a member of (see Bot.mentionable). Elsewhere the mention
// would show as "@unknown-user", so the plain name is used.
func who(userID, name string, mention bool) string {
	if mention {
		return "<@" + userID + ">"
	}
	return "**" + escape(name) + "**"
}

// partyFill is " (3/5)": how full the party is, counting the leader and
// players found outside the bot.
func partyFill(d config.Dungeon, p lfg.Post) string {
	size := d.PartySizeFor(p.ModeID)
	if size <= 0 {
		return ""
	}
	return " (" + strconv.Itoa(max(1, size-p.Missing)) + "/" + strconv.Itoa(size) + ")"
}

// rosterText lists the party and the applicants, each marked with the
// colour of their role, as seen on viewerGuildID's copy of the post. The
// leader's private panel passes leaderView to link every player.
func (b *Bot) rosterText(p lfg.Post, viewerGuildID string, leaderView bool, l i18n.Lang) string {
	line := func(pl lfg.Player) string {
		c := b.game.ClassOrUnknown(pl.ClassID)
		s := b.roleBullet(pl.RoleID) + " "
		if leaderView {
			s += "**" + escape(pl.Name) + "** <@" + pl.UserID + ">"
		} else {
			s += who(pl.UserID, pl.Name, b.mentionable(pl.UserID, pl.GuildID, viewerGuildID))
		}
		s += " — " + strings.TrimSpace(c.Emoji+" "+c.Name)
		if pl.GS > 0 {
			s += " · GS " + l.GS(pl.GS)
		}
		if pl.GuildName != "" && pl.GuildID != viewerGuildID {
			s += " · *" + escape(truncate(pl.GuildName, 32)) + "*"
		}
		return s
	}
	list := func(players []lfg.Player) string {
		var sb strings.Builder
		for n, pl := range players {
			if n == maxListed {
				sb.WriteString(l.T("roster.more", len(players)-maxListed))
				break
			}
			sb.WriteString("\n" + line(pl))
		}
		return sb.String()
	}

	var sb strings.Builder
	if len(p.Accepted) > 0 {
		sb.WriteString(l.T("roster.in_party", len(p.Accepted)) + list(p.Accepted) + "\n")
	}
	if len(p.Applicants) > 0 {
		sb.WriteString(l.T("roster.waiting", len(p.Applicants)) + list(p.Applicants))
	} else if p.Status == lfg.StatusOpen {
		sb.WriteString(l.T("roster.empty_open"))
	}
	return orDefault(strings.TrimSpace(sb.String()), l.T("roster.empty"))
}

// draftPanel is the private /lfg form. The difficulty menu only appears
// once a dungeon is chosen, because every dungeon has its own modes.
func (b *Bot) draftPanel(d draft, l i18n.Lang) discordgo.MessageComponent {
	dungeon, chosen := b.game.Dungeon(d.DungeonID)

	comps := []discordgo.MessageComponent{
		text(l.T("draft.title")),
		text(l.T("draft.dungeon")),
		selectRow(draftID(fieldDungeon), l.T("draft.dungeon_ph"), 1, b.dungeonOptions(d.DungeonID, l)),
	}
	if chosen && len(dungeon.Modes) > 0 {
		comps = append(comps,
			text(l.T("draft.mode")),
			selectRow(draftID(fieldMode), l.T("draft.mode_ph"), 1, modeOptions(dungeon, d.ModeID, l)))
	}
	comps = append(comps,
		text(l.T("draft.looking")),
		selectRow(draftID(fieldLookingFor), l.T("draft.looking_ph"), maxSelectValues, b.lookingForOptions(d.LookingFor, l)),
		text(l.T("draft.missing")),
		selectRow(draftID(fieldMissing), l.T("draft.missing_ph"), 1, missingOptions(b.draftMaxMissing(d), d.Missing, l)),
		divider(),
	)

	gs := l.T("draft.gs_none")
	if d.MinGS > 0 {
		gs = l.GS(d.MinGS) + "+"
	}
	if game := b.gameMinGS(d); game > 0 {
		gs += l.T("draft.gs_game", l.GS(game))
	}
	comps = append(comps, text(l.T("draft.summary", gs,
		startText(d.StartAt, "", l), orDefault(escape(truncate(d.Note, 120)), l.T("draft.no_note")))))
	if d.Problem != "" {
		comps = append(comps, text("⚠️ "+l.T(d.Problem)))
	}

	comps = append(comps, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{CustomID: draftID(fieldDetails), Label: l.T("draft.details"), Style: discordgo.SecondaryButton,
			Emoji: emoji("✏️")},
		discordgo.Button{CustomID: draftID(fieldPost), Label: l.T("draft.post"), Style: discordgo.SuccessButton,
			Emoji: emoji("📣"), Disabled: !chosen || len(d.LookingFor) == 0},
		discordgo.Button{CustomID: draftID(fieldCancel), Label: l.T("draft.cancel"), Style: discordgo.SecondaryButton},
	}})
	return container(colorOpen, comps...)
}

func (b *Bot) detailsModal(d draft, l i18n.Lang) modal {
	optional := false
	gsHelp := l.T("details.gs_help")
	if game := b.gameMinGS(d); game > 0 {
		gsHelp = l.T("details.gs_game", l.GS(game))
	}
	gs := ""
	if d.MinGS > 0 {
		gs = strconv.Itoa(d.MinGS)
	}
	return modal{
		CustomID: draftID(fieldDetails),
		Title:    l.T("details.title"),
		Components: []discordgo.MessageComponent{
			discordgo.Label{
				Label:       l.T("details.gs"),
				Description: truncate(gsHelp, 100),
				Component: discordgo.TextInput{CustomID: "gs", Style: discordgo.TextInputShort, Value: gs,
					Placeholder: l.T("details.gs_ph"), Required: &optional, MaxLength: 8},
			},
			discordgo.Label{
				Label:       l.T("details.start"),
				Description: truncate(l.T("details.start_help", zoneLabel(d.zone(), time.Now())), 100),
				Component: discordgo.TextInput{CustomID: "start", Style: discordgo.TextInputShort, Value: d.StartInput,
					Placeholder: l.T("details.start_ph"), Required: &optional, MaxLength: 30},
			},
			discordgo.Label{
				Label: l.T("details.note"),
				Component: discordgo.TextInput{CustomID: "note", Style: discordgo.TextInputParagraph, Value: d.Note,
					Placeholder: l.T("details.note_ph"), Required: &optional, MaxLength: 300},
			},
		},
	}
}

// joinModal asks a player for their class, the role they fill with it, and
// their gear score. Only combinations the post is looking for are offered,
// and the player's saved profile is filled in.
func (b *Bot) joinModal(p lfg.Post, prof lfg.Profile, l i18n.Lang) modal {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	gs := ""
	if prof.GS > 0 {
		gs = strconv.Itoa(prof.GS)
	}
	required := p.MinGS > 0
	gsHelp := l.T("join.gs_optional")
	if required {
		gsHelp = l.T("join.gs_required", l.GS(p.MinGS))
	}
	return modal{
		CustomID: customID(actJoinForm, p.ID),
		Title:    truncate(l.T("join.title", dungeon.Name), 45),
		Components: []discordgo.MessageComponent{
			discordgo.Label{
				Label:       l.T("join.choice"),
				Description: truncate(l.T("join.looking", b.lookingForNames(p.LookingFor, l)), 100),
				Component: modalSelect{CustomID: "choice", Placeholder: l.T("join.choice_ph"),
					MinValues: 1, MaxValues: 1, Options: b.choiceOptions(b.game.Choices(p.LookingFor), prof)},
			},
			discordgo.Label{
				Label:       l.T("join.gs"),
				Description: gsHelp,
				Component: discordgo.TextInput{CustomID: "gs", Style: discordgo.TextInputShort, Value: gs,
					Placeholder: l.T("join.gs_ph"), Required: &required, MaxLength: 8},
			},
		},
	}
}

// managePanel is the leader's private control panel for a post.
func (b *Bot) managePanel(p lfg.Post, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	head := l.T("manage.title", escape(dungeon.Name))
	if line := difficultyLine(dungeon, p.ModeID, p.MinGS, l); line != "" {
		head += line + "\n"
	}
	switch p.Status {
	case lfg.StatusOpen:
		head += l.T("manage.looking", p.Missing, partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
	case lfg.StatusFull:
		head += l.T("manage.full")
	default:
		head += l.T("manage.ended")
	}

	deleteButton := discordgo.Button{CustomID: customID(actDelete, p.ID), Label: l.T("manage.delete"),
		Style: discordgo.DangerButton, Emoji: emoji("🗑️")}
	comps := []discordgo.MessageComponent{text(head), divider(), text(b.rosterText(p, "", true, l))}
	if !p.Active() {
		comps = append(comps,
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{deleteButton}},
			text(l.T("manage.disappears", p.ClosedAt.Add(keepEndedPosts).Unix())))
		return container(statusColor(p.Status), comps...)
	}

	if len(p.Applicants) > 0 && p.Missing > 0 {
		comps = append(comps, selectRow(customID(actAccept, p.ID), l.T("manage.accept_ph"),
			min(len(p.Applicants), p.Missing), b.playerOptions(p.Applicants, l)))
	}
	if all := slices.Concat(p.Accepted, p.Applicants); len(all) > 0 {
		comps = append(comps, selectRow(customID(actKick, p.ID), l.T("manage.kick_ph"),
			len(all), b.playerOptions(all, l)))
	}
	comps = append(comps,
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actLess, p.ID), Label: l.T("manage.less"),
				Style: discordgo.SecondaryButton, Disabled: p.Missing == 0},
			discordgo.Button{CustomID: customID(actMore, p.ID), Label: l.T("manage.more"),
				Style: discordgo.SecondaryButton, Disabled: p.Missing >= dungeon.MaxMissing(p.ModeID, b.game.MaxMissing)},
			discordgo.Button{CustomID: customID(actClose, p.ID), Label: l.T("manage.close"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🔒")},
			deleteButton,
		}},
		text(l.T("manage.footer")),
	)
	return container(statusColor(p.Status), comps...)
}

// confirmDelete asks the leader before a post disappears everywhere.
func (b *Bot) confirmDelete(p lfg.Post, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	return container(colorClosed,
		text(l.T("delete.confirm", escape(dungeon.Name))),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actDeleteYes, p.ID), Label: l.T("delete.yes"), Style: discordgo.DangerButton},
			discordgo.Button{CustomID: customID(actPanel, p.ID), Label: l.T("delete.keep"), Style: discordgo.SecondaryButton},
		}},
	)
}

// dungeonOptions describes each dungeon as "Expedition · 5 players ·
// GS 700–2,600": its type from the config, then the party size and gear
// score range in the reader's language.
func (b *Bot) dungeonOptions(selected string, l i18n.Lang) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(b.game.Dungeons))
	for _, d := range b.game.Dungeons {
		var desc []string
		if d.Description != "" {
			desc = append(desc, d.Description)
		}
		if d.PartySize > 0 {
			desc = append(desc, l.N(d.PartySize, "n.player"))
		}
		if gs := gsRange(d, l); gs != "" {
			desc = append(desc, gs)
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: d.Name, Value: d.ID, Description: truncate(strings.Join(desc, " · "), 100),
			Emoji: emoji(d.Emoji), Default: d.ID == selected,
		})
	}
	return opts
}

// gsRange is "GS 700–2,600" across a dungeon's modes.
func gsRange(d config.Dungeon, l i18n.Lang) string {
	lo, hi := 0, 0
	for _, m := range d.Modes {
		if m.GS == 0 {
			continue
		}
		if lo == 0 || m.GS < lo {
			lo = m.GS
		}
		hi = max(hi, m.GS)
	}
	switch {
	case lo == 0:
		return ""
	case lo == hi:
		return "GS " + l.GS(lo)
	}
	return "GS " + l.GS(lo) + "–" + l.GS(hi)
}

func modeOptions(d config.Dungeon, selected string, l i18n.Lang) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(d.Modes))
	for _, m := range d.Modes {
		var desc []string
		if m.GS > 0 {
			desc = append(desc, "GS "+l.GS(m.GS)+"+")
		}
		if size := d.PartySizeFor(m.ID); size > 0 {
			desc = append(desc, l.N(size, "n.player"))
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: m.Name, Value: m.ID, Description: strings.Join(desc, " · "), Default: m.ID == selected,
		})
	}
	return opts
}

func missingOptions(maxMissing, selected int, l i18n.Lang) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, maxMissing)
	for n := 1; n <= maxMissing; n++ {
		opts = append(opts, discordgo.SelectMenuOption{
			Label: l.N(n, "n.player"), Value: strconv.Itoa(n), Default: n == selected,
		})
	}
	return opts
}

// lookingForOptions lists the roles first, then "Any class", then every
// class with the roles it can fill.
func (b *Bot) lookingForOptions(selected []string, l i18n.Lang) []discordgo.SelectMenuOption {
	var opts []discordgo.SelectMenuOption
	for _, r := range b.game.Roles {
		var classes []string
		for _, c := range b.game.Classes {
			if slices.Contains(c.Roles, r.ID) {
				classes = append(classes, c.Name)
			}
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: r.Name, Value: r.ID, Description: truncate(strings.Join(classes, ", "), 100),
			Emoji: emoji(r.Emoji), Default: slices.Contains(selected, r.ID),
		})
	}
	opts = append(opts, discordgo.SelectMenuOption{
		Label: l.T("any.name"), Value: config.AnyID, Description: l.T("any.desc"),
		Emoji: emoji(anyEmoji), Default: slices.Contains(selected, config.AnyID),
	})
	for _, c := range b.game.Classes {
		opts = append(opts, discordgo.SelectMenuOption{
			Label: c.Name, Value: c.ID, Description: b.roleNames(c.Roles),
			Emoji: emoji(c.Emoji), Default: slices.Contains(selected, c.ID),
		})
	}
	return opts
}

const anyEmoji = "🎲"

// want resolves one "Looking for" entry: a role, a class, or anyone.
func (b *Bot) want(id string, l i18n.Lang) (name, emoji string) {
	if id == config.AnyID {
		return l.T("any.name"), anyEmoji
	}
	if r, ok := b.game.Role(id); ok {
		return r.Name, r.Emoji
	}
	c := b.game.ClassOrUnknown(id)
	return c.Name, c.Emoji
}

// lookingForList is "🟦 Tank · 🟩 Healer · ✨ Cleric".
func (b *Bot) lookingForList(ids []string, l i18n.Lang) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		name, emoji := b.want(id, l)
		parts = append(parts, strings.TrimSpace(emoji+" "+name))
	}
	return strings.Join(parts, " · ")
}

// lookingForNames is lookingForList without emojis, for plain text fields.
func (b *Bot) lookingForNames(ids []string, l i18n.Lang) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name, _ := b.want(id, l)
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func (b *Bot) roleNames(ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if r, ok := b.game.Role(id); ok {
			names = append(names, r.Name)
		}
	}
	return strings.Join(names, " / ")
}

// roleBullet marks a player with their role's colour.
func (b *Bot) roleBullet(roleID string) string {
	if r, ok := b.game.Role(roleID); ok && r.Emoji != "" {
		return r.Emoji
	}
	return "•"
}

// bringing is how a player joins: "🟦 Tank · ⚔️ Gladiator".
func (b *Bot) bringing(pl lfg.Player) string { return b.choiceText(pl.ClassID, pl.RoleID) }

func (b *Bot) playerOptions(players []lfg.Player, l i18n.Lang) []discordgo.SelectMenuOption {
	players = players[:min(len(players), maxSelectValues)]
	opts := make([]discordgo.SelectMenuOption, 0, len(players))
	for _, pl := range players {
		c := b.game.ClassOrUnknown(pl.ClassID)
		desc := c.Name
		icon := c.Emoji
		if r, ok := b.game.Role(pl.RoleID); ok {
			desc = r.Name + " · " + desc
			icon = orDefault(r.Emoji, icon)
		}
		if pl.GS > 0 {
			desc += " · GS " + l.GS(pl.GS)
		}
		if pl.GuildName != "" {
			desc += " · " + pl.GuildName
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: truncate(pl.Name, 100), Value: pl.UserID, Description: truncate(desc, 100), Emoji: emoji(icon),
		})
	}
	return opts
}

// Discord allows at most 25 options per select menu.
const maxSelectValues = 25

func selectRow(id, placeholder string, maxValues int, opts []discordgo.SelectMenuOption) discordgo.ActionsRow {
	one := 1
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
		MenuType: discordgo.StringSelectMenu, CustomID: id, Placeholder: placeholder,
		MinValues: &one, MaxValues: min(maxValues, len(opts)), Options: opts,
	}}}
}

func statusColor(s lfg.Status) int {
	switch s {
	case lfg.StatusOpen:
		return colorOpen
	case lfg.StatusFull:
		return colorFull
	}
	return colorClosed
}

func container(color int, comps ...discordgo.MessageComponent) discordgo.Container {
	return discordgo.Container{AccentColor: &color, Components: comps}
}

func text(s string) discordgo.TextDisplay { return discordgo.TextDisplay{Content: s} }

func divider() discordgo.Separator {
	on, size := true, discordgo.SeparatorSpacingSizeSmall
	return discordgo.Separator{Divider: &on, Spacing: &size}
}

// notice is a one-line container used for confirmations and errors.
func notice(color int, s string) discordgo.MessageComponent { return container(color, text(s)) }

var customEmoji = regexp.MustCompile(`^<(a?):(\w+):(\d+)>$`)

// emoji accepts a unicode emoji or a custom one written as <:name:id>.
func emoji(s string) *discordgo.ComponentEmoji {
	if s == "" {
		return nil
	}
	if m := customEmoji.FindStringSubmatch(s); m != nil {
		return &discordgo.ComponentEmoji{Name: m[2], ID: m[3], Animated: m[1] == "a"}
	}
	return &discordgo.ComponentEmoji{Name: s}
}

// zeroWidthSpace after "@" stops @everyone and @here from working.
const zeroWidthSpace = string(rune(0x200B))

var markdownChars = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, ">", `\>`, "#", `\#`,
	"@", "@"+zeroWidthSpace)

// escape keeps user-written text from breaking the layout or pinging anyone.
func escape(s string) string { return markdownChars.Replace(s) }

func quote(s string) string { return "> " + strings.ReplaceAll(s, "\n", "\n> ") }

// threadName names the thread under a post, e.g. "Krao Cave · Conquest ·
// Leader". Thread names are plain text of up to 100 characters.
func (b *Bot) threadName(p lfg.Post) string {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	parts := []string{dungeon.Name}
	if m, ok := dungeon.Mode(p.ModeID); ok {
		parts = append(parts, m.Name)
	}
	if p.LeaderName != "" {
		parts = append(parts, p.LeaderName)
	}
	return truncate(strings.Join(parts, " · "), 100)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
