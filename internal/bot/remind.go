package bot

import (
	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// remind pings the party shortly before a planned start: the leader and
// the players accepted by then, each on their own server or by DM, except
// those who turned reminders off in /profile. Players accepted later were
// pinged with the start already, and a run nobody was accepted for stays
// quiet.
func (b *Bot) remind(postID string) {
	p, ok := b.store.Post(postID)
	if !ok || !p.Active() || len(p.Accepted) == 0 {
		return
	}
	pings := map[string][]string{}
	for _, pl := range p.Party() {
		if prof, ok := b.store.Profile(pl.UserID); ok && prof.SkipReminders {
			continue
		}
		pings[pl.GuildID] = append(pings[pl.GuildID], pl.UserID)
	}
	if len(pings) == 0 {
		return
	}

	b.learnMembers(p, p.GuildIDs())
	if p.Rally {
		b.remindRally(p, pings)
		return
	}
	b.pingOrDM(p, pings, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.reminderNote(p, guildID, l)
	}, b.reminderDM(p))
}

// remindRally pings a rally's players by their mentions, as its roster
// may be too long to name them all, mentionBatch per message.
func (b *Bot) remindRally(p lfg.Post, pings map[string][]string) {
	b.pingInBatches(p, pings, func(_ string, users []string, _ bool, l i18n.Lang) discordgo.MessageComponent {
		if len(users) == 0 {
			return nil
		}
		return b.rallyReminderNote(p, users, l)
	}, b.reminderDM(p))
}

// reminderDM sends a player the reminder by DM.
func (b *Bot) reminderDM(p lfg.Post) func(guildID, userID string) bool {
	where, at := b.runName(p), p.StartAt.Unix()
	return func(guildID, userID string) bool {
		l := b.guildLang(guildID)
		return b.dm(userID, notice(colorFull, l.T("dm.reminder", where, at, at)+leaderCharLine(p, l)))
	}
}
