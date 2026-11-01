package bot

import (
	"cmp"
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

	// Keeps the card well under the 4000 character limit for text displays,
	// whatever the names and icons on it.
	rosterBudget = 2800
)

// Post components use "lfg:<action>:<postID>". The dungeon change also
// carries the leader's pick after the post's ID, as ":<dungeonID>" or
// ":<dungeonID>:<modeID>".
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
	actEdit      = "edit"
	actEditForm  = "editform"
	actReopen    = "reopen"
	actMove      = "move"
	actMoveTo    = "moveto"
	actMoveMode  = "movemode"
	actMoveYes   = "moveyes"
	actSize      = "size"
	actLooking   = "looking"
	// The handover carries the new leader after the post's ID, and on the
	// confirm buttons whether the old one stays: ":<userID>:stay".
	actHandTo  = "handto"
	actHandYes = "handyes"
	// Moderators of a server the post shows on.
	actHide     = "hide"
	actBlock    = "block"
	actNetBlock = "netblock"
)

// The /lfg panel uses "lfgd:<field>", the /lfg-abyss panel "lfga:<field>".
// They are ephemeral, so the draft is found by the user who clicks.
const (
	draftPrefix      = "lfgd:"
	abyssDraftPrefix = "lfga:"
	fieldDungeon     = "dungeon"
	fieldMode        = "mode"
	fieldLookingFor  = "lookingfor"
	fieldMissing     = "missing"
	fieldSize        = "size"
	fieldPlays       = "plays"
	fieldFaction     = "faction"
	fieldDetails     = "details"
	fieldPost        = "post"
	fieldCancel      = "cancel"
)

func customID(action, postID string) string { return "lfg:" + action + ":" + postID }

func draftID(field string, abyss bool) string {
	if abyss {
		return abyssDraftPrefix + field
	}
	return draftPrefix + field
}

// rallySizes are the group sizes a rally can have, the leader included;
// zero means no limit.
var rallySizes = []int{0, 5, 10, 15, 20, 25, 30, 40, 50, 60, 80, 100}

// textFor picks the Abyss wording of a text for a rally: "draft.dungeon"
// becomes "draft.abyss_dungeon". Both catalogs have both.
func textFor(key string, rally bool) string {
	if !rally {
		return key
	}
	group, name, _ := strings.Cut(key, ".")
	return group + ".abyss_" + name
}

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
	header := "## " + strings.TrimSpace(dungeon.Emoji+" "+dungeon.Name)
	if line := withSide(difficultyLine(dungeon, p.ModeID, p.MinGS, l), b.sideLine(p, l)); line != "" {
		header += "\n" + line
	}
	header += "\n" + b.statusLine(p, l)

	info := l.T("card.start") + postStart(p, l)

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

	join := discordgo.Button{CustomID: customID(actJoin, p.ID), Label: l.T("card.join"), Style: discordgo.SuccessButton,
		Emoji: emoji("✋"), Disabled: !p.Active()}
	if p.Status == lfg.StatusFull {
		join.Label, join.Style, join.Emoji = l.T("card.join_reserve"), discordgo.PrimaryButton, emoji("🪑")
	}
	body = append(body, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		join,
		discordgo.Button{CustomID: customID(actLeave, p.ID), Label: l.T("card.leave"), Style: discordgo.SecondaryButton,
			Disabled: !p.Active()},
		// Manage stays on after the post ends, so the leader can reopen it.
		manageButton(p.ID, true, l),
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

// statusLine is how the party stands, as the card shows it: "Looking for
// 2 more (3/5) — 🟦 Tank", full, closed or expired.
func (b *Bot) statusLine(p lfg.Post, l i18n.Lang) string {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	switch p.Status {
	case lfg.StatusOpen:
		if p.Rally {
			return l.T("card.abyss_looking", len(p.Party()), partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
		}
		return l.T("card.looking", p.Missing, partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
	case lfg.StatusFull:
		return l.T(textFor("card.full", p.Rally)) + partyFill(dungeon, p) + l.T("card.reserves_welcome")
	case lfg.StatusClosed:
		return l.T("card.closed")
	}
	return l.T("card.expired")
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
// players found outside the bot. A rally counts its players against its
// size, and shows nothing without one.
func partyFill(d config.Dungeon, p lfg.Post) string {
	size := d.PartySizeFor(p.ModeID)
	filled := max(1, size-p.Missing)
	if p.Rally {
		size, filled = p.Size, len(p.Party())
	}
	if size <= 0 {
		return ""
	}
	return " (" + strconv.Itoa(filled) + "/" + strconv.Itoa(size) + ")"
}

// rosterText lists the party, by role, then the applicants and the
// reserves in the order they joined, each marked with the colour of their
// role, as seen on viewerGuildID's copy of the post. The leader's private
// panel passes leaderView to link every player. Players who don't fit in
// rosterBudget are counted instead.
func (b *Bot) rosterText(p lfg.Post, viewerGuildID string, leaderView bool, l i18n.Lang) string {
	room := rosterBudget
	list := func(players []lfg.Player) string {
		return playerList(players, func(pl lfg.Player) string { return b.memberLine(p, pl, viewerGuildID, leaderView, l) }, &room, l)
	}

	party := b.partyByRole(p)
	var sb strings.Builder
	sb.WriteString(l.T(textFor("roster.in_party", p.Rally), len(party)) + list(party) + "\n")
	if len(p.Applicants) > 0 {
		sb.WriteString(l.T("roster.waiting", len(p.Applicants)) + list(p.Applicants) + "\n")
	} else if p.Status == lfg.StatusOpen && len(p.Accepted) == 0 && len(p.Reserves) == 0 {
		sb.WriteString(l.T("roster.empty_open"))
	}
	if len(p.Reserves) > 0 {
		sb.WriteString(l.T("roster.reserves", len(p.Reserves)) + list(p.Reserves))
	}
	return strings.TrimSpace(sb.String())
}

// memberLine is one player on a post, as seen on viewerGuildID's copy:
// the colour of their role, their name, character, class, gear score and
// home server. The leader wears the crown, which stands in for the colour
// when they didn't pick a role. leaderView links every player.
func (b *Bot) memberLine(p lfg.Post, pl lfg.Player, viewerGuildID string, leaderView bool, l i18n.Lang) string {
	s := b.roleBullet(b.shownRole(p, pl)) + " "
	if pl.UserID == p.LeaderID {
		s = strings.TrimPrefix(s, "• ") + b.game.LeaderEmoji + " "
	}
	if leaderView {
		s += "**" + escape(pl.Name) + "** <@" + pl.UserID + ">"
	} else {
		s += who(pl.UserID, pl.Name, b.mentionable(pl.UserID, pl.GuildID, viewerGuildID))
	}
	s += charSuffix(pl.Char)
	// Only a leader who didn't pick a class has none.
	if pl.ClassID != "" {
		c := b.game.ClassOrUnknown(pl.ClassID)
		s += " — " + strings.TrimSpace(c.Emoji+" "+c.Name)
	}
	if pl.GS > 0 {
		s += " · GS " + l.GS(pl.GS)
	}
	if pl.GuildName != "" && pl.GuildID != viewerGuildID {
		s += " · *" + escape(truncate(pl.GuildName, 32)) + "*"
	}
	return s
}

// playerList writes one line per player while they fit in room
// characters, which it uses up, and counts the rest.
func playerList(players []lfg.Player, line func(lfg.Player) string, room *int, l i18n.Lang) string {
	var sb strings.Builder
	for n, pl := range players {
		s := "\n" + line(pl)
		if size := utf8.RuneCountInString(s); size <= *room {
			*room -= size
			sb.WriteString(s)
			continue
		}
		*room = 0
		sb.WriteString(l.T("roster.more", len(players)-n))
		break
	}
	return sb.String()
}

// partyByRole is the party in the order of the roles in game.json, by the
// role each player shows as: tanks, then DPS, then healers. A leader who
// didn't pick a role comes first. Players with the same role keep their
// order, so the leader leads their role and the others follow in the
// order they were accepted.
func (b *Bot) partyByRole(p lfg.Post) []lfg.Player {
	party := p.Party()
	rank := make(map[string]int, len(party))
	for _, pl := range party {
		role := b.shownRole(p, pl)
		rank[pl.UserID] = slices.IndexFunc(b.game.Roles, func(r config.Role) bool { return r.ID == role })
	}
	slices.SortStableFunc(party, func(a, c lfg.Player) int { return cmp.Compare(rank[a.UserID], rank[c.UserID]) })
	return party
}

// draftPanel is the private /lfg form, or the /lfg-abyss one, which asks
// for an Abyss event and a group size instead of a dungeon and the missing
// players. The difficulty (or zone) menu only appears once a dungeon is
// chosen, because every dungeon has its own modes.
func (b *Bot) draftPanel(d draft, l i18n.Lang) discordgo.MessageComponent {
	dungeon, chosen := b.draftDungeon(d)
	id := func(field string) string { return draftID(field, d.Abyss) }
	key := func(k string) string { return l.T(textFor(k, d.Abyss)) }

	comps := []discordgo.MessageComponent{
		text(key("draft.title")),
		text(key("draft.dungeon")),
		selectRow(id(fieldDungeon), key("draft.dungeon_ph"), 1, b.dungeonOptions(b.game.Content(d.Abyss), d.DungeonID, l)),
	}
	if chosen && len(dungeon.Modes) > 0 {
		comps = append(comps,
			text(key("draft.mode")),
			selectRow(id(fieldMode), key("draft.mode_ph"), 1, modeOptions(dungeon, d.ModeID, l)))
	}
	comps = append(comps,
		text(l.T("draft.looking")),
		selectRow(id(fieldLookingFor), l.T("draft.looking_ph"), maxSelectValues, b.lookingForOptions(d.LookingFor, l)))
	if d.Abyss {
		comps = append(comps,
			text(l.T("draft.size")),
			selectRow(id(fieldSize), l.T("draft.size_ph"), 1, sizeOptions(d.Size, 1, l)))
	} else {
		comps = append(comps,
			text(l.T("draft.missing")),
			selectRow(id(fieldMissing), l.T("draft.missing_ph"), 1, missingOptions(b.draftMaxMissing(d), d.Missing, l)))
	}
	comps = append(comps,
		text(l.T("draft.plays")),
		selectRow(id(fieldPlays), l.T("draft.plays_ph"), 1,
			b.choiceOptions(b.game.Choices([]string{config.AnyID}), profileChoice(lfg.Profile{ClassID: d.Class, RoleID: d.Role}))),
	)
	if len(b.game.Factions) > 0 {
		comps = append(comps,
			text(l.T("draft.faction")),
			selectRow(id(fieldFaction), l.T("draft.faction_ph"), 1, b.factionOptions(d.Faction)))
	}
	comps = append(comps, divider())

	start := startText(d.StartAt, "", l)
	if d.Abyss && d.StartAt.IsZero() {
		start = l.T("card.abyss_start_default")
	}
	summary := detailsSummary(d.MinGS, b.gameMinGS(d), start, d.Note, l)
	if d.Abyss {
		summary += "\n" + serverSummary(d.Server, l)
	}
	comps = append(comps, text(summary))
	if d.Problem != "" {
		comps = append(comps, text("⚠️ "+l.T(d.Problem)))
	}

	comps = append(comps, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{CustomID: id(fieldDetails), Label: l.T("draft.details"), Style: discordgo.SecondaryButton,
			Emoji: emoji("✏️")},
		discordgo.Button{CustomID: id(fieldPost), Label: l.T("draft.post"), Style: discordgo.SuccessButton,
			Emoji: emoji("📣"), Disabled: !chosen || len(d.LookingFor) == 0 || b.needsFaction(d.Faction)},
		discordgo.Button{CustomID: id(fieldCancel), Label: l.T("draft.cancel"), Style: discordgo.SecondaryButton},
	}})
	return container(colorOpen, comps...)
}

// sizeOptions offers the group sizes of a rally: no limit, then every
// size that holds at least least players.
func sizeOptions(selected, least int, l i18n.Lang) []discordgo.SelectMenuOption {
	opts := []discordgo.SelectMenuOption{{Label: l.T("draft.size_none"), Value: "0",
		Description: l.T("draft.size_none_desc"), Emoji: emoji("👥"), Default: selected == 0}}
	for _, n := range rallySizes {
		if n > 0 && n >= least {
			opts = append(opts, discordgo.SelectMenuOption{Label: l.N(n, "n.player"), Value: strconv.Itoa(n), Default: n == selected})
		}
	}
	return opts
}

// needsFaction reports whether a post still has to say which faction it is
// for: whenever game.json lists factions.
func (b *Bot) needsFaction(faction string) bool {
	return len(b.game.Factions) > 0 && b.knownFaction(faction) == ""
}

// serverSummary is the game server line of a rally's details.
func serverSummary(server string, l i18n.Lang) string {
	return l.T("draft.server", orDefault(escape(server), l.T("draft.server_none")))
}

func (b *Bot) detailsModal(d draft, l i18n.Lang) modal {
	var server *string
	if d.Abyss {
		server = &d.Server
	}
	return detailsForm(draftID(fieldDetails, d.Abyss), b.gameMinGS(d), d.MinGS, d.StartInput, d.zone(), d.Note, server, l)
}

// editModal is the same form for a post that is already up, filled in
// with its current details.
func (b *Bot) editModal(p lfg.Post, zone *time.Location, l i18n.Lang) modal {
	m, _ := b.game.DungeonOrUnknown(p.DungeonID).Mode(p.ModeID)
	var server *string
	if p.Rally {
		server = &p.Server
	}
	return detailsForm(customID(actEditForm, p.ID), m.GS, p.MinGS, startInput(p.StartAt, zone), zone, p.Note, server, l)
}

// detailsForm asks for the minimum gear score, the start and a note, and
// for a rally, which takes server, the game server it gathers on. gameGS
// is what the game asks for the difficulty, shown as a hint.
func detailsForm(id string, gameGS, minGS int, start string, zone *time.Location, note string, server *string, l i18n.Lang) modal {
	optional := false
	gsHelp := l.T("details.gs_help")
	if gameGS > 0 {
		gsHelp = l.T("details.gs_game", l.GS(gameGS))
	}
	gs := ""
	if minGS > 0 {
		gs = strconv.Itoa(minGS)
	}
	m := modal{
		CustomID: id,
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
				Description: truncate(l.T("details.start_help", zoneLabel(zone, time.Now())), 100),
				Component: discordgo.TextInput{CustomID: "start", Style: discordgo.TextInputShort, Value: start,
					Placeholder: l.T("details.start_ph"), Required: &optional, MaxLength: 30},
			},
			discordgo.Label{
				Label: l.T("details.note"),
				Component: discordgo.TextInput{CustomID: "note", Style: discordgo.TextInputParagraph, Value: note,
					Placeholder: l.T("details.note_ph"), Required: &optional, MaxLength: 300},
			},
		},
	}
	if server != nil {
		m.Components = append(m.Components, serverInput(*server, l.T("details.server_help"), false, l))
	}
	return m
}

// startInput writes a start the way the form reads it back, in the
// leader's time zone.
func startInput(at time.Time, zone *time.Location) string {
	if at.IsZero() {
		return ""
	}
	return at.In(zone).Format("02.01.2006 15:04")
}

// detailsSummary is the gear score, start and note of a draft or a post;
// start is already written out.
func detailsSummary(minGS, gameGS int, start, note string, l i18n.Lang) string {
	gs := l.T("draft.gs_none")
	if minGS > 0 {
		gs = l.GS(minGS) + "+"
	}
	if gameGS > 0 {
		gs += l.T("draft.gs_game", l.GS(gameGS))
	}
	return l.T("draft.summary", gs, start, orDefault(escape(truncate(note, 120)), l.T("draft.no_note")))
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
	gsHelp := l.T(textFor("join.gs_optional", p.Rally))
	if required {
		gsHelp = l.T("join.gs_required", l.GS(p.MinGS))
	}
	title := l.T("join.title", dungeon.Name)
	if p.Status == lfg.StatusFull {
		title = l.T("join.title_reserve", dungeon.Name)
	}
	m := modal{
		CustomID: customID(actJoinForm, p.ID),
		Title:    truncate(title, 45),
		Components: []discordgo.MessageComponent{
			discordgo.Label{
				Label:       l.T("join.choice"),
				Description: truncate(l.T("join.looking", b.lookingForNames(p.LookingFor, l)), 100),
				Component: modalSelect{CustomID: "choice", Placeholder: l.T("join.choice_ph"),
					MinValues: 1, MaxValues: 1, Options: b.choiceOptions(b.game.Choices(p.LookingFor), profileChoice(prof))},
			},
			discordgo.Label{
				Label:       l.T("join.gs"),
				Description: gsHelp,
				Component: discordgo.TextInput{CustomID: "gs", Style: discordgo.TextInputShort, Value: gs,
					Placeholder: l.T("join.gs_ph"), Required: &required, MaxLength: 8},
			},
			charInput(prof.Char, l),
		},
	}
	// The faction and game server are asked only when the profile doesn't
	// already say the player fits: on an alt, or before they saved them.
	if f, ok := b.game.Faction(p.Faction); ok && prof.Faction != p.Faction {
		m.Components = append(m.Components, discordgo.Label{
			Label:       l.T("join.faction"),
			Description: truncate(l.T("join.faction_help", f.Name), 100),
			Component: modalSelect{CustomID: "faction", Placeholder: l.T("join.faction_ph"), MinValues: 1, MaxValues: 1,
				Options: b.factionOptions(prof.Faction)},
		})
	}
	if p.Rally && p.Server != "" && !sameServer(prof.Server, p.Server) {
		m.Components = append(m.Components, serverInput(prof.Server, l.T("join.server_help", p.Server), true, l))
	}
	return m
}

// managePanel is the leader's private control panel for a post.
func (b *Bot) managePanel(p lfg.Post, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	head := l.T("manage.title", escape(dungeon.Name))
	if line := difficultyLine(dungeon, p.ModeID, 0, l); line != "" {
		head += line + "\n"
	}
	switch {
	case p.Status == lfg.StatusOpen && p.Rally:
		head += b.statusLine(p, l)
	case p.Status == lfg.StatusOpen:
		head += l.T("manage.looking", p.Missing, partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
	case p.Status == lfg.StatusFull:
		head += l.T(textFor("manage.full", p.Rally))
	default:
		head += l.T("manage.ended")
	}

	deleteButton := discordgo.Button{CustomID: customID(actDelete, p.ID), Label: l.T("manage.delete"),
		Style: discordgo.DangerButton, Emoji: emoji("🗑️")}
	comps := []discordgo.MessageComponent{text(head), text(detailsSummary(p.MinGS, 0, postStart(p, l), p.Note, l)),
		divider(), text(b.rosterText(p, "", true, l))}
	if !p.Active() {
		comps = append(comps,
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{CustomID: customID(actReopen, p.ID), Label: l.T("manage.reopen"), Style: discordgo.SuccessButton,
					Emoji: emoji("🔓")},
				deleteButton,
			}},
			text(l.T("manage.disappears", p.ClosedAt.Add(keepEndedPosts).Unix())+"\n"+l.T("manage.ended_reopen")))
		return container(statusColor(p.Status), comps...)
	}
	if p.Rally {
		return container(statusColor(p.Status), append(comps, b.rallyControls(p, deleteButton, l)...)...)
	}

	if waiting := p.Waiting(); len(waiting) > 0 && p.Missing > 0 {
		comps = append(comps, selectRow(customID(actAccept, p.ID), l.T("manage.accept_ph"),
			min(len(waiting), p.Missing), b.playerOptions(p, waiting, l)))
	}
	if all := slices.Concat(p.Accepted, p.Waiting()); len(all) > 0 {
		comps = append(comps, selectRow(customID(actKick, p.ID), l.T("manage.kick_ph"),
			len(all), b.playerOptions(p, all, l)))
	}
	comps = append(comps, b.lookingAndHandOver(p, l)...)
	comps = append(comps,
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actEdit, p.ID), Label: l.T("draft.details"), Style: discordgo.SecondaryButton,
				Emoji: emoji("✏️")},
			discordgo.Button{CustomID: customID(actMove, p.ID), Label: l.T("manage.move"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🗺️")},
			discordgo.Button{CustomID: customID(actLess, p.ID), Label: l.T("manage.less"),
				Style: discordgo.SecondaryButton, Disabled: p.Missing == 0},
			discordgo.Button{CustomID: customID(actMore, p.ID), Label: l.T("manage.more"),
				Style: discordgo.SecondaryButton, Disabled: p.Missing >= dungeon.MaxMissing(p.ModeID, b.game.MaxMissing)},
		}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actClose, p.ID), Label: l.T("manage.close"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🔒")},
			deleteButton,
		}},
		text(l.T("manage.footer")),
	)
	return container(statusColor(p.Status), comps...)
}

// rallyControls are the leader's controls for an open rally: remove
// players, change the group size, and the buttons. Nobody waits to be
// accepted, as players join straight in.
func (b *Bot) rallyControls(p lfg.Post, deleteButton discordgo.Button, l i18n.Lang) []discordgo.MessageComponent {
	var comps []discordgo.MessageComponent
	// The menu holds 25 players. In a bigger rally it offers the latest to
	// join, the likeliest to be removed.
	if all := slices.Concat(p.Accepted, p.Reserves); len(all) > 0 {
		all = all[max(0, len(all)-maxSelectValues):]
		comps = append(comps, selectRow(customID(actKick, p.ID), l.T("manage.abyss_kick_ph"), len(all), b.playerOptions(p, all, l)))
	}
	comps = append(comps, b.lookingAndHandOver(p, l)...)
	return append(comps,
		text(l.T("draft.size")),
		selectRow(customID(actSize, p.ID), l.T("draft.size_ph"), 1, sizeOptions(p.Size, len(p.Party()), l)),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actEdit, p.ID), Label: l.T("draft.details"), Style: discordgo.SecondaryButton,
				Emoji: emoji("✏️")},
			discordgo.Button{CustomID: customID(actMove, p.ID), Label: l.T("manage.abyss_move"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🗺️")},
			discordgo.Button{CustomID: customID(actClose, p.ID), Label: l.T("manage.close"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🔒")},
			deleteButton,
		}},
		text(l.T("manage.abyss_footer")),
	)
}

// lookingAndHandOver are the menus that change whom an open post is
// looking for and hand it over to a party member. A rally offers the first
// 25 to join, who have been with it the longest.
func (b *Bot) lookingAndHandOver(p lfg.Post, l i18n.Lang) []discordgo.MessageComponent {
	comps := []discordgo.MessageComponent{
		text(l.T("draft.looking")),
		selectRow(customID(actLooking, p.ID), l.T("draft.looking_ph"), maxSelectValues, b.lookingForOptions(p.LookingFor, l)),
	}
	if len(p.Accepted) > 0 {
		comps = append(comps, b.handOverMenu(p, l.T("manage.hand_ph"), l))
	}
	return comps
}

// handOverMenu offers the party members who can take over a post.
func (b *Bot) handOverMenu(p lfg.Post, placeholder string, l i18n.Lang) discordgo.ActionsRow {
	return selectRow(customID(actHandTo, p.ID), placeholder, 1, b.playerOptions(p, p.Accepted, l))
}

// handOverPanel asks before a party member takes over a post: from the
// leader, who stays in the party or leaves it, or from moderators, for
// whom the leader may have gone missing.
func (b *Bot) handOverPanel(p lfg.Post, userID string, byLeader bool, l i18n.Lang) discordgo.MessageComponent {
	i := slices.IndexFunc(p.Accepted, func(pl lfg.Player) bool { return pl.UserID == userID })
	if i < 0 || !p.Active() {
		return notice(colorClosed, errorText(lfg.ErrNotInParty, l))
	}
	next := p.Accepted[i]
	title, stay, leave := l.T("hand.title", b.game.LeaderEmoji, b.runName(p)), l.T("hand.stay"), l.T("hand.leave")
	if !byLeader {
		name := escape(truncate(p.LeaderName, 32))
		title = l.T("hand.mod_title", b.game.LeaderEmoji, b.runName(p), name)
		stay, leave = l.T("hand.mod_stay", p.LeaderName), l.T("hand.mod_leave", p.LeaderName)
	}
	confirm := func(how, label string, style discordgo.ButtonStyle) discordgo.Button {
		return discordgo.Button{CustomID: customID(actHandYes, p.ID+":"+userID+":"+how), Label: truncate(label, 80), Style: style}
	}
	return container(statusColor(p.Status),
		text(title+"\n"+l.T("hand.who", b.playerLine(p, next, l))+"\n"+l.T("hand.help")),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			confirm("stay", stay, discordgo.SuccessButton),
			confirm("leave", leave, discordgo.PrimaryButton),
			discordgo.Button{CustomID: customID(actPanel, p.ID), Label: l.T("move.back"), Style: discordgo.SecondaryButton},
		}},
	)
}

// confirmDelete asks the leader, or a moderator, before a post disappears
// everywhere.
func (b *Bot) confirmDelete(p lfg.Post, mod bool, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	ask := l.T("delete.confirm", escape(dungeon.Name))
	if mod {
		ask = l.T("delete.mod_confirm", escape(dungeon.Name), escape(p.LeaderName))
	}
	return container(colorClosed,
		text(ask),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: customID(actDeleteYes, p.ID), Label: l.T("delete.yes"), Style: discordgo.DangerButton},
			discordgo.Button{CustomID: customID(actPanel, p.ID), Label: l.T("delete.keep"), Style: discordgo.SecondaryButton},
		}},
	)
}

// movePanel lets the leader take the post, with everyone on it, to
// another dungeon or mode. The pick travels in the custom IDs until the
// leader confirms it, so nothing is kept in memory.
//
// A rally moves among the Abyss events only, and a dungeon post among the
// dungeons.
func (b *Bot) movePanel(p lfg.Post, dungeonID, modeID string, l i18n.Lang) discordgo.MessageComponent {
	key := func(k string) string { return l.T(textFor(k, p.Rally)) }
	to, chosen := b.game.Dungeon(dungeonID)
	if chosen = chosen && to.Abyss == p.Rally; !chosen {
		dungeonID, modeID = "", ""
	} else if _, ok := to.Mode(modeID); !ok {
		modeID = firstMode(to)
	}

	comps := []discordgo.MessageComponent{
		text(key("move.title") + "\n" + l.T(textFor("move.help", p.Rally), b.runName(p))),
		text(key("draft.dungeon")),
		selectRow(customID(actMoveTo, p.ID), key("draft.dungeon_ph"), 1, b.dungeonOptions(b.game.Content(p.Rally), dungeonID, l)),
	}
	if chosen && len(to.Modes) > 0 {
		comps = append(comps,
			text(key("draft.mode")),
			selectRow(customID(actMoveMode, p.ID+":"+dungeonID), key("draft.mode_ph"), 1, modeOptions(to, modeID, l)))
	}

	confirm := discordgo.Button{CustomID: customID(actMoveYes, p.ID+":"+dungeonID+":"+modeID), Label: key("move.yes"),
		Style: discordgo.SuccessButton, Emoji: emoji("✅")}
	moved := p
	fromSize := b.game.DungeonOrUnknown(p.DungeonID).PartySizeFor(p.ModeID)
	size := to.PartySizeFor(modeID)
	switch {
	case !chosen || !p.Active() || dungeonID == p.DungeonID && modeID == p.ModeID:
		confirm.Disabled = true
	// With the post active, a move only fails when the party doesn't fit.
	case moved.Move(dungeonID, modeID, fromSize, size, to.MaxMissing(modeID, b.game.MaxMissing)) != nil:
		confirm.Disabled = true
		moved.DungeonID, moved.ModeID = dungeonID, modeID
		comps = append(comps, divider(),
			text("⚠️ "+l.T("move.too_big", l.N(p.Filled(fromSize), "n.player"), b.runName(moved), size)))
	default:
		comps = append(comps, divider(), text(l.T("move.preview", b.runName(moved), b.statusLine(moved, l))))
	}
	// Custom IDs hold at most 100 characters.
	confirm.Disabled = confirm.Disabled || len(confirm.CustomID) > 100

	comps = append(comps, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		confirm,
		discordgo.Button{CustomID: customID(actPanel, p.ID), Label: l.T("move.back"), Style: discordgo.SecondaryButton},
	}})
	return container(statusColor(p.Status), comps...)
}

// firstMode is the easiest mode of a dungeon, or none.
func firstMode(d config.Dungeon) string {
	if len(d.Modes) == 0 {
		return ""
	}
	return d.Modes[0].ID
}

// dungeonOptions describes each dungeon as "Expedition · 5 players ·
// GS 700–2,600": its type from the config, then the party size and gear
// score range in the reader's language. Abyss events have no party size.
func (b *Bot) dungeonOptions(dungeons []config.Dungeon, selected string, l i18n.Lang) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(dungeons))
	for _, d := range dungeons {
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

// bringing is how a player is in p: "🟦 Tank · ⚔️ Gladiator".
func (b *Bot) bringing(p lfg.Post, pl lfg.Player) string {
	return b.choiceText(pl.ClassID, b.shownRole(p, pl))
}

// shownRole is the role a player shows as on p. A backup, like a Chanter
// healing, shows as its main role while someone in the party fills the
// role for real, like the leader or an accepted Cleric.
func (b *Bot) shownRole(p lfg.Post, pl lfg.Player) string {
	instead, ok := b.game.Backup(pl.ClassID, pl.RoleID)
	if !ok {
		return pl.RoleID
	}
	for _, m := range p.Party() {
		if _, backup := b.game.Backup(m.ClassID, m.RoleID); m.RoleID == pl.RoleID && !backup {
			return instead
		}
	}
	return pl.RoleID
}

func (b *Bot) playerOptions(p lfg.Post, players []lfg.Player, l i18n.Lang) []discordgo.SelectMenuOption {
	players = players[:min(len(players), maxSelectValues)]
	opts := make([]discordgo.SelectMenuOption, 0, len(players))
	for _, pl := range players {
		c := b.game.ClassOrUnknown(pl.ClassID)
		desc := c.Name
		icon := c.Emoji
		if r, ok := b.game.Role(b.shownRole(p, pl)); ok {
			desc = r.Name + " · " + desc
			icon = orDefault(r.Emoji, icon)
		}
		if pl.UserID == p.LeaderID {
			icon = b.game.LeaderEmoji
		}
		if pl.GS > 0 {
			desc += " · GS " + l.GS(pl.GS)
		}
		if pl.GuildName != "" {
			desc += " · " + pl.GuildName
		}
		label := pl.Name
		if pl.Char != "" {
			label += " · " + pl.Char
		}
		if p.IsReserve(pl.UserID) {
			label = "🪑 " + label
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: truncate(label, 100), Value: pl.UserID, Description: truncate(desc, 100), Emoji: emoji(icon),
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
