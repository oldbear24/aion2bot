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

// /profile lets a player save their main role, class and gear score. The
// join form starts from it.
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
					Options: b.choiceOptions(b.game.Choices([]string{config.AnyID}), prof)},
			},
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
		},
	}
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
	prof := lfg.Profile{UserID: userID, ClassID: classID, RoleID: roleID, GS: gs, TimeZone: tz}
	if err := b.store.SetProfile(prof); err != nil {
		b.reply(i, notice(colorClosed, errorText(err, l)))
		return err
	}
	saved := b.choiceText(classID, roleID)
	if gs > 0 {
		saved += " · GS " + l.GS(gs)
	}
	if tz != "" {
		saved += " · 🕒 " + l.T("tz."+tz)
	}
	return b.reply(i, notice(colorOpen, l.T("profile.saved", saved)+l.T("profile.saved_hint")))
}

// rememberJoin keeps the player's profile current: the gear score always
// follows their latest join, while a main role and class they saved
// themselves stays.
func (b *Bot) rememberJoin(pl lfg.Player) {
	prof, ok := b.store.Profile(pl.UserID)
	if ok && pl.GS == 0 {
		return
	}
	if !ok {
		prof = lfg.Profile{UserID: pl.UserID, ClassID: pl.ClassID, RoleID: pl.RoleID}
	}
	if pl.GS > 0 {
		prof.GS = pl.GS
	}
	if err := b.store.SetProfile(prof); err != nil {
		log.Printf("save profile of %s: %v", pl.UserID, err)
	}
}

// choiceOptions turns class/role combinations into menu options, with the
// player's saved one pre-selected when it is on offer.
func (b *Bot) choiceOptions(choices []config.Choice, prof lfg.Profile) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(choices))
	for _, c := range choices {
		opts = append(opts, discordgo.SelectMenuOption{
			Label:   strings.TrimPrefix(c.Role.Name+" · "+c.Class.Name, " · "),
			Value:   c.Class.ID + "|" + c.Role.ID,
			Emoji:   emoji(orDefault(c.Role.Emoji, c.Class.Emoji)),
			Default: prof.ClassID == c.Class.ID && prof.RoleID == c.Role.ID,
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
