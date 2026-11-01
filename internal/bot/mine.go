package bot

import (
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// mineLimit is how many runs /lfg-mine lists, each a section with a
// button, well within Discord's 40 components.
const mineLimit = 8

// /lfg-mine lists a player's runs: those they lead, with Manage, those
// they are on, with Leave, and those they led that ended, which they can
// still reopen.
func (b *Bot) cmdMine(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	return b.reply(i, b.minePanel(b.store.PostsOf(userID), userID, i.GuildID, lang(i)))
}

func (b *Bot) minePanel(posts []lfg.Post, userID, guildID string, l i18n.Lang) discordgo.MessageComponent {
	var leading, joined, ended []lfg.Post
	for _, p := range posts {
		switch {
		case p.LeaderID == userID && p.Active():
			leading = append(leading, p)
		case p.LeaderID == userID:
			ended = append(ended, p)
		case p.Active():
			joined = append(joined, p)
		}
	}
	now := time.Now()
	sortRuns(leading, now)
	sortRuns(joined, now)

	comps := []discordgo.MessageComponent{text(l.T("mine.title"))}
	if len(leading)+len(joined)+len(ended) == 0 {
		return container(colorClosed, append(comps, text(l.T("mine.none", b.cmd("lfg-find"), b.cmd("lfg"))))...)
	}
	room := mineLimit
	section := func(head string, list []lfg.Post, entry func(lfg.Post) discordgo.MessageComponent) {
		if len(list) == 0 {
			return
		}
		comps = append(comps, text(head))
		for n, p := range list {
			if room == 0 {
				comps = append(comps, text(l.T("roster.more", len(list)-n)))
				return
			}
			room--
			comps = append(comps, entry(p))
		}
	}
	section(l.T("mine.leading", b.game.LeaderEmoji, len(leading)), leading, func(p lfg.Post) discordgo.MessageComponent {
		return b.mineEntry(p, guildID, b.shortStatus(p, l), manageButton(p.ID, false, l), l)
	})
	section(l.T("mine.joined", len(joined)), joined, func(p lfg.Post) discordgo.MessageComponent {
		leave := discordgo.Button{CustomID: customID(actLeave, p.ID), Label: l.T("card.leave"), Style: discordgo.SecondaryButton}
		return b.mineEntry(p, guildID, b.mineStanding(p, userID, l)+" · "+b.shortStatus(p, l), leave, l)
	})
	section(l.T("mine.ended", len(ended)), ended, func(p lfg.Post) discordgo.MessageComponent {
		return b.mineEntry(p, guildID, b.shortStatus(p, l)+" · "+l.T("mine.reopen_until", p.ClosedAt.Add(keepEndedPosts).Unix()),
			manageButton(p.ID, false, l), l)
	})
	comps = append(comps, text(l.T("mine.footer")))
	return container(colorFull, comps...)
}

// mineEntry is one run on /lfg-mine: where to and how it stands, when it
// starts, its leader and a link to its copy, with a button beside it.
func (b *Bot) mineEntry(p lfg.Post, guildID, standing string, button discordgo.Button, l i18n.Lang) discordgo.MessageComponent {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	s := "**" + strings.TrimSpace(dungeon.Emoji+" "+dungeon.Name) + "**"
	if line := withSide(difficultyLine(dungeon, p.ModeID, p.MinGS, l), b.sideLine(p, l)); line != "" {
		s += " · " + line
	}
	s += "\n" + standing + "\n🕒 " + postStart(p, l) + " · " + b.game.LeaderEmoji + " " + escape(truncate(p.LeaderName, 32))
	if ref, ok := b.findCopy(p, guildID); ok {
		s += " · [" + l.T("find.open") + "](" + messageLink(ref) + ")"
	}
	return discordgo.Section{Components: []discordgo.MessageComponent{text(s)}, Accessory: button}
}

// shortStatus is how a run stands, like the card's status line without
// whom it is looking for: "Looking for 2 more (3/5)".
func (b *Bot) shortStatus(p lfg.Post, l i18n.Lang) string {
	fill := partyFill(b.game.DungeonOrUnknown(p.DungeonID), p)
	switch p.Status {
	case lfg.StatusOpen:
		if p.Rally {
			return l.T("mine.abyss_open", len(p.Party()), fill)
		}
		return l.T("mine.open", p.Missing, fill)
	case lfg.StatusFull:
		return l.T(textFor("card.full", p.Rally)) + fill
	}
	return b.statusLine(p, l)
}

// mineStanding is where a player stands on a run they joined: in the
// party, waiting for the leader, or a reserve and which.
func (b *Bot) mineStanding(p lfg.Post, userID string, l i18n.Lang) string {
	switch {
	case p.InParty(userID):
		return l.T(textFor("mine.in_party", p.Rally))
	case p.IsReserve(userID):
		for n, pl := range p.Reserves {
			if pl.UserID == userID {
				return l.T("mine.reserve", n+1)
			}
		}
	}
	return l.T("mine.waiting")
}
