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

const idProfileModal = "lfgp:save"

// maxChar is the longest in-game character name the bot keeps.
const maxChar = 24

// /profile lets a player save their main role, class, character name and
// gear score, which the join form starts from, plus their time zone and
// whether they want reminders before runs start.
func (b *Bot) cmdProfile(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	prof, _ := b.store.Profile(userID)
	if prof.TimeZone == "" {
		prof.TimeZone = guessZone(string(i.Locale))
	}
	return b.showModal(i, b.profileModal(prof, lang(i)))
}

func (b *Bot) profileModal(prof lfg.Profile, l i18n.Lang) modal {
	optional := false
	gs := ""
	if prof.GS > 0 {
		gs = strconv.Itoa(prof.GS)
	}
	return modal{
		CustomID: idProfileModal,
		Title:    l.T("profile.title"),
		Components: []discordgo.MessageComponent{
			discordgo.Label{
				Label:       l.T("profile.choice"),
				Description: l.T("profile.choice_help"),
				Component: modalSelect{CustomID: "choice", Placeholder: l.T("profile.choice_ph"), MinValues: 1, MaxValues: 1,
					Options: b.choiceOptions(b.game.Choices([]string{config.AnyID}), profileChoice(prof))},
			},
			charInput(prof.Char, l),
			discordgo.Label{
				Label:       l.T("profile.gs"),
				Description: l.T("profile.gs_help"),
				Component: discordgo.TextInput{CustomID: "gs", Style: discordgo.TextInputShort, Value: gs,
					Placeholder: l.T("join.gs_ph"), Required: &optional, MaxLength: 8},
			},
			discordgo.Label{
				Label:       l.T("profile.tz"),
				Description: l.T("profile.tz_help"),
				Component: modalSelect{CustomID: "tz", Placeholder: l.T("profile.tz_ph"), MinValues: 1, MaxValues: 1,
					Options: zoneOptions(prof.TimeZone, l, time.Now())},
			},
			discordgo.Label{
				Label:       l.T("profile.remind"),
				Description: truncate(l.T("profile.remind_help", l.N(b.game.ReminderMinutes, "n.minutes")), 100),
				Component: modalSelect{CustomID: "remind", MinValues: 1, MaxValues: 1, Options: []discordgo.SelectMenuOption{
					{Label: l.T("profile.remind_on"), Value: "on", Emoji: emoji("⏰"), Default: !prof.SkipReminders},
					{Label: l.T("profile.remind_off"), Value: "off", Emoji: emoji("🔕"), Default: prof.SkipReminders},
				}},
			},
		},
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

func (b *Bot) onProfileSubmit(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	values := modalValues(i.ModalSubmitData().Components)
	classID, roleID, _ := strings.Cut(first(values["choice"]), "|")
	gs, err := parseGS(first(values["gs"]))
	switch {
	case !b.game.Fits([]string{config.AnyID}, classID, roleID):
		return b.reply(i, notice(colorClosed, l.T("profile.bad_choice")))
	case err != nil:
		return b.reply(i, notice(colorClosed, l.T("profile.bad_gs")))
	}

	tz := first(values["tz"])
	if !slices.Contains(timeZones, tz) {
		tz = ""
	}
	prof := lfg.Profile{UserID: userID, ClassID: classID, RoleID: roleID, GS: gs, TimeZone: tz,
		Char: cleanChar(first(values["char"])), SkipReminders: first(values["remind"]) == "off"}
	if err := b.store.SetProfile(prof); err != nil {
		b.reply(i, notice(colorClosed, errorText(err, l)))
		return err
	}
	saved := b.choiceText(classID, roleID)
	if prof.Char != "" {
		saved += " · 🎮 " + charTag(prof.Char)
	}
	if gs > 0 {
		saved += " · GS " + l.GS(gs)
	}
	if tz != "" {
		saved += " · 🕒 " + l.T("tz."+tz)
	}
	if prof.SkipReminders {
		saved += " · 🔕"
	}
	return b.reply(i, notice(colorOpen, l.T("profile.saved", saved)+l.T("profile.saved_hint")))
}

// rememberJoin keeps the player's profile current: the gear score and
// character name follow their latest join, while a main role and class
// they saved themselves stays.
func (b *Bot) rememberJoin(pl lfg.Player) {
	prof, ok := b.store.Profile(pl.UserID)
	if ok && pl.GS == 0 && pl.Char == "" {
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
	if err := b.store.SetProfile(prof); err != nil {
		log.Printf("save profile of %s: %v", pl.UserID, err)
	}
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
