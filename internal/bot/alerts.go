package bot

import (
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// /lfg-notify sets up a DM about new runs: for the dungeons a player picks,
// looking for something they play, with a gear score their profile meets.
// Posts that reach the server they set it up on count.

// The /lfg-notify panel uses "lfgn:<field>". Every change is saved at once.
const (
	alertPrefix   = "lfgn:"
	alertDungeons = "dungeons"
	alertAs       = "as"
	alertOff      = "off"
)

func alertID(field string) string { return alertPrefix + field }

func (b *Bot) cmdNotify(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	prof, _ := b.store.Profile(userID)
	return b.reply(i, b.alertPanel(b.alertOf(userID, prof), prof, lang(i)))
}

// alertOf is the player's saved alert, or a new one that starts from what
// they play according to /profile.
func (b *Bot) alertOf(userID string, prof lfg.Profile) lfg.Alert {
	if a, ok := b.store.Alert(userID); ok {
		return a
	}
	return lfg.Alert{UserID: userID, Choices: profileChoice(prof)}
}

func (b *Bot) onAlert(i *discordgo.Interaction, field string, values []string) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	prof, _ := b.store.Profile(userID)
	if field == alertOff {
		b.store.RemoveAlert(userID)
		return b.update(i, b.alertPanel(b.alertOf(userID, prof), prof, l))
	}

	a := b.alertOf(userID, prof)
	// The alert moves to the server it was last changed on, in the
	// language of the player's Discord.
	a.GuildID, a.Lang = i.GuildID, string(l)
	switch field {
	case alertDungeons:
		a.Dungeons = slices.DeleteFunc(slices.Clone(values), func(id string) bool {
			_, ok := b.game.Dungeon(id)
			return !ok
		})
	case alertAs:
		a.Choices = slices.DeleteFunc(slices.Clone(values), func(c string) bool {
			classID, roleID, _ := strings.Cut(c, "|")
			return !b.game.Fits([]string{config.AnyID}, classID, roleID)
		})
	}
	if err := b.store.SetAlert(a); err != nil {
		b.update(i, notice(colorClosed, errorText(err, l)))
		return err
	}
	return b.update(i, b.alertPanel(a, prof, l))
}

func (b *Bot) alertPanel(a lfg.Alert, prof lfg.Profile, l i18n.Lang) discordgo.MessageComponent {
	dungeons := b.dungeonOptions("", l)
	for n := range dungeons {
		dungeons[n].Default = slices.Contains(a.Dungeons, dungeons[n].Value)
	}
	choices := b.game.Choices([]string{config.AnyID})

	status := l.T("alert.off")
	if a.On() {
		var names []string
		for _, d := range b.game.Dungeons {
			if slices.Contains(a.Dungeons, d.ID) {
				names = append(names, d.Name)
			}
		}
		var plays []string
		for _, c := range a.Choices {
			classID, roleID, _ := strings.Cut(c, "|")
			plays = append(plays, b.choiceText(classID, roleID))
		}
		status = l.T("alert.on", escape(strings.Join(names, ", ")), strings.Join(plays, ", "))
		if prof.GS > 0 {
			status += l.T("alert.gs", l.GS(prof.GS), b.cmd("profile"))
		}
	}

	return container(colorFull,
		text(l.T("alert.title")),
		text(l.T("alert.dungeons")),
		selectRow(alertID(alertDungeons), l.T("alert.dungeons_ph"), len(dungeons), dungeons),
		text(l.T("alert.as")),
		selectRow(alertID(alertAs), l.T("alert.as_ph"), len(choices), b.choiceOptions(choices, a.Choices)),
		divider(),
		text(status),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{CustomID: alertID(alertOff), Label: l.T("alert.turn_off"), Style: discordgo.SecondaryButton,
				Emoji: emoji("🔕"), Disabled: len(a.Dungeons) == 0},
		}},
		text(l.T("alert.footer")),
	)
}

// sendAlerts DMs the players whose alerts match a new post, with a link to
// its copy on the server each of them set the alert up on.
func (b *Bot) sendAlerts(postID string) {
	p, ok := b.store.Post(postID)
	if !ok || !p.Active() {
		return
	}
	var due []lfg.Alert
	for _, a := range b.store.AlertsIn(p.GuildIDs()) {
		if a.UserID != p.LeaderID && b.alertMatches(a, p) {
			due = append(due, a)
		}
	}
	forEach(due, func(a lfg.Alert) {
		ref, _ := p.MessageIn(a.GuildID)
		b.dm(a.UserID, b.alertDM(p, ref, i18n.Parse(a.Lang)))
	})
}

// alertMatches reports whether a post is one the alert asks for: one of
// its dungeons, looking for one of its choices, and with a minimum gear
// score the player's profile meets, when they saved one.
func (b *Bot) alertMatches(a lfg.Alert, p lfg.Post) bool {
	if !slices.Contains(a.Dungeons, p.DungeonID) {
		return false
	}
	if p.MinGS > 0 {
		if prof, ok := b.store.Profile(a.UserID); ok && prof.GS > 0 && prof.GS < p.MinGS {
			return false
		}
	}
	return slices.ContainsFunc(a.Choices, func(c string) bool {
		classID, roleID, _ := strings.Cut(c, "|")
		return b.game.Fits(p.LookingFor, classID, roleID)
	})
}

// alertDM is the post in short, with a link to its copy on the player's
// server.
func (b *Bot) alertDM(p lfg.Post, ref lfg.MessageRef, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	s := l.T("alert.dm_title", strings.TrimSpace(dungeon.Emoji+" "+dungeon.Name))
	if line := difficultyLine(dungeon, p.ModeID, p.MinGS, l); line != "" {
		s += "\n" + line
	}
	s += "\n" + l.T("card.looking", p.Missing, partyFill(dungeon, p), b.lookingForList(p.LookingFor, l))
	s += "\n" + b.game.LeaderEmoji + " " + l.T("card.leader") + "**" + escape(p.LeaderName) + "**"
	if p.GuildName != "" {
		s += " · *" + escape(truncate(p.GuildName, 40)) + "*"
	}
	s += "\n" + l.T("card.start") + startText(p.StartAt, p.Start, l)
	return container(colorOpen,
		text(s),
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: l.T("alert.open"), Style: discordgo.LinkButton, URL: messageLink(ref)},
		}},
		text(l.T("alert.dm_footer")),
	)
}

// messageLink opens a copy of a post in Discord.
func messageLink(ref lfg.MessageRef) string {
	return "https://discord.com/channels/" + ref.GuildID + "/" + ref.ChannelID + "/" + ref.MessageID
}
