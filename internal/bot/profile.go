package bot

import (
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// The /profile panel uses "lfgp:<field>"; every change is saved at once.
// Its button opens the form for what is typed, whose answer is
// idProfileModal.
const (
	profilePrefix  = "lfgp:"
	profChoice     = "choice"
	profFaction    = "faction"
	profZone       = "tz"
	profRemind     = "remind"
	profEdit       = "edit"
	idProfileModal = profilePrefix + "save"
)

// maxChar is the longest in-game character or game server name the bot
// keeps.
const maxChar = 24

// /profile shows what a player saved about themselves, to change it: their
// main role and class, which the join form starts from, their faction,
// character, game server and gear score, which /lfg-find and /lfg-notify
// match runs to, their time zone, and whether they want reminders before
// runs start.
func (b *Bot) cmdProfile(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	prof, _ := b.store.Profile(userID)
	return b.reply(i, b.profilePanel(prof, string(i.Locale), "", lang(i)))
}

// profilePanel shows a profile with a menu for each choice and a button for
// what is typed. Until the player picks a time zone, it shows the one
// guessed from their Discord language, which start times are read in.
func (b *Bot) profilePanel(prof lfg.Profile, locale, done string, l i18n.Lang) discordgo.MessageComponent {
	unset := l.T("profile.unset")
	plays := unset
	if prof.ClassID != "" {
		plays = b.choiceText(prof.ClassID, prof.RoleID)
	}
	char := orDefault(charTag(prof.Char), unset)
	gs := unset
	if prof.GS > 0 {
		gs = l.GS(prof.GS)
	}
	summary := l.T("profile.plays", plays) + "\n" + l.T("profile.char_line", char) + "\n" +
		l.T("profile.server_line", orDefault(escape(prof.Server), unset)) + "\n" + l.T("profile.gs_line", gs)
	zone := prof.TimeZone
	if zone == "" {
		zone = guessZone(locale)
	}

	head := l.T("profile.title_panel")
	if done != "" {
		head += "\n" + done
	}
	comps := []discordgo.MessageComponent{
		text(head),
		text(summary),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: profilePrefix + profEdit, Label: l.T("profile.edit"), Style: discordgo.PrimaryButton,
				Emoji: emoji("✏️")},
		}},
		divider(),
		text(l.T("profile.choice_label")),
		selectRow(profilePrefix+profChoice, l.T("profile.choice_ph"), 1,
			b.choiceOptions(b.game.Choices([]string{config.AnyID}), profileChoice(prof))),
	}
	if len(b.game.Factions) > 0 {
		comps = append(comps,
			text(l.T("profile.faction_label")),
			selectRow(profilePrefix+profFaction, l.T("profile.faction_ph"), 1, b.factionOptions(prof.Faction)))
	}
	comps = append(comps,
		text(l.T("profile.tz_label")),
		selectRow(profilePrefix+profZone, l.T("profile.tz_ph"), 1, zoneOptions(zone, l, time.Now())),
		text(l.T("profile.remind_label", l.N(b.game.ReminderMinutes, "n.minutes"))),
		selectRow(profilePrefix+profRemind, l.T("profile.remind"), 1, []discordgo.SelectMenuOption{
			{Label: l.T("profile.remind_on"), Value: "on", Emoji: emoji("⏰"), Default: !prof.SkipReminders},
			{Label: l.T("profile.remind_off"), Value: "off", Emoji: emoji("🔕"), Default: prof.SkipReminders},
		}),
		text(l.T("profile.footer")),
	)
	return container(colorFull, comps...)
}

// profileForm asks for what a player types about themselves: their
// character's name and game server, and their gear score.
func (b *Bot) profileForm(prof lfg.Profile, l i18n.Lang) modal {
	optional := false
	gs := ""
	if prof.GS > 0 {
		gs = strconv.Itoa(prof.GS)
	}
	return modal{
		CustomID: idProfileModal,
		Title:    l.T("profile.title"),
		Components: []discordgo.MessageComponent{
			charInput(prof.Char, l),
			serverInput(prof.Server, l.T("profile.server_help"), false, l),
			discordgo.Label{
				Label:       l.T("profile.gs"),
				Description: l.T("profile.gs_help"),
				Component: discordgo.TextInput{CustomID: "gs", Style: discordgo.TextInputShort, Value: gs,
					Placeholder: l.T("join.gs_ph"), Required: &optional, MaxLength: 8},
			},
		},
	}
}

// onProfile saves a choice made on the /profile panel, or opens the form.
func (b *Bot) onProfile(i *discordgo.Interaction, field string, values []string) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	prof, ok := b.store.Profile(userID)
	if !ok {
		prof = lfg.Profile{UserID: userID}
	}
	value := first(values)
	switch field {
	case profEdit:
		return b.showModal(i, b.profileForm(prof, l))
	case profChoice:
		classID, roleID, _ := strings.Cut(value, "|")
		if !b.game.Fits([]string{config.AnyID}, classID, roleID) {
			return b.update(i, b.profilePanel(prof, string(i.Locale), "⚠️ "+l.T("profile.bad_choice"), l))
		}
		prof.ClassID, prof.RoleID = classID, roleID
	case profFaction:
		if faction := b.knownFaction(value); faction != "" {
			prof.Faction = faction
		}
	case profZone:
		if slices.Contains(timeZones, value) {
			prof.TimeZone = value
		}
	case profRemind:
		prof.SkipReminders = value == "off"
	}
	return b.updateProfile(i, prof)
}

// onProfileSubmit saves the character, game server and gear score from the
// form opened on the /profile panel.
func (b *Bot) onProfileSubmit(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	values := modalValues(i.ModalSubmitData().Components)
	prof, ok := b.store.Profile(userID)
	if !ok {
		prof = lfg.Profile{UserID: userID}
	}
	gs, err := parseGS(first(values["gs"]))
	if err != nil {
		return b.update(i, b.profilePanel(prof, string(i.Locale), "⚠️ "+l.T("profile.bad_gs"), l))
	}
	prof.GS, prof.Char, prof.Server = gs, cleanChar(first(values["char"])), cleanChar(first(values["server"]))
	return b.updateProfile(i, prof)
}

// updateProfile saves a profile and redraws the panel it was changed on.
func (b *Bot) updateProfile(i *discordgo.Interaction, prof lfg.Profile) error {
	l := lang(i)
	if err := b.store.SetProfile(prof); err != nil {
		b.update(i, notice(colorClosed, errorText(err, l)))
		return err
	}
	return b.update(i, b.profilePanel(prof, string(i.Locale), l.T("profile.saved_now"), l))
}

func (b *Bot) saveProfile(prof lfg.Profile) {
	if err := b.store.SetProfile(prof); err != nil {
		log.Printf("save profile of %s: %v", prof.UserID, err)
	}
}

// charInput asks for the in-game character name, in /profile and the join
// form.
func charInput(char string, l i18n.Lang) discordgo.Label {
	optional := false
	return discordgo.Label{
		Label:       l.T("profile.char"),
		Description: l.T("profile.char_help"),
		Component: discordgo.TextInput{CustomID: "char", Style: discordgo.TextInputShort, Value: char,
			Placeholder: l.T("profile.char_ph"), Required: &optional, MaxLength: maxChar},
	}
}

// cleanChar keeps a typed character name on one line and without
// backticks, which would break the code span it is shown in.
func cleanChar(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
	return truncate(s, maxChar)
}

// charTag shows a character name so it is easy to copy for an in-game
// whisper or invite.
func charTag(char string) string {
	if char == "" {
		return ""
	}
	return "`" + char + "`"
}

// charSuffix is " · `Name`" after a player's Discord name.
func charSuffix(char string) string {
	if char == "" {
		return ""
	}
	return " · " + charTag(char)
}

// rememberJoin keeps the player's profile current: the gear score,
// character name, faction and game server follow their latest join, as
// they belong to the character they joined with, while a main role and
// class they saved themselves stays.
func (b *Bot) rememberJoin(pl lfg.Player, faction, server string) {
	prof, ok := b.store.Profile(pl.UserID)
	if ok && pl.GS == 0 && pl.Char == "" && faction == "" && server == "" {
		return
	}
	if !ok {
		prof = lfg.Profile{UserID: pl.UserID, ClassID: pl.ClassID, RoleID: pl.RoleID}
	}
	if pl.GS > 0 {
		prof.GS = pl.GS
	}
	if pl.Char != "" {
		prof.Char = pl.Char
	}
	if faction != "" {
		prof.Faction = faction
	}
	if server != "" {
		prof.Server = server
	}
	b.saveProfile(prof)
}

// profileChoice is the class/role pair a player saved, as menus use it.
func profileChoice(prof lfg.Profile) []string {
	if prof.ClassID == "" {
		return nil
	}
	return []string{prof.ClassID + "|" + prof.RoleID}
}

// choiceOptions turns class/role combinations into menu options, with the
// selected "class|role" pairs pre-selected when they are on offer.
func (b *Bot) choiceOptions(choices []config.Choice, selected []string) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(choices))
	for _, c := range choices {
		value := c.Class.ID + "|" + c.Role.ID
		opts = append(opts, discordgo.SelectMenuOption{
			Label:   strings.TrimPrefix(c.Role.Name+" · "+c.Class.Name, " · "),
			Value:   value,
			Emoji:   emoji(orDefault(c.Role.Emoji, c.Class.Emoji)),
			Default: slices.Contains(selected, value),
		})
	}
	return opts
}

// choiceText is "🟦 Tank · ⚔️ Gladiator", or just the class without a role.
func (b *Bot) choiceText(classID, roleID string) string {
	c := b.game.ClassOrUnknown(classID)
	s := strings.TrimSpace(c.Emoji + " " + c.Name)
	if r, ok := b.game.Role(roleID); ok {
		s = strings.TrimSpace(r.Emoji+" "+r.Name) + " · " + s
	}
	return s
}
