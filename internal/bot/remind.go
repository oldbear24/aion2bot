package bot

import (
	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// mentionBatch is how many users one rally reminder pings; Discord
// notifies at most 100 users per message.
const mentionBatch = 50

// remind pings the party shortly before a planned start: the leader and
// the players accepted by then, each on their own server, except those who
// turned reminders off in /profile. Players accepted later were pinged with
// the start already, and a run nobody was accepted for stays quiet.
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
	reached := b.announce(p, pings, true, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.reminderNote(p, guildID, l)
	})
	b.remindByDM(p, pings, reached)
}

// remindRally pings a rally's players by their mentions, as its roster
// may be too long to name them all, mentionBatch per message.
func (b *Bot) remindRally(p lfg.Post, pings map[string][]string) {
	for lo := 0; ; lo += mentionBatch {
		batch := map[string][]string{}
		for guildID, users := range pings {
			if lo < len(users) {
				batch[guildID] = users[lo:min(len(users), lo+mentionBatch)]
			}
		}
		if len(batch) == 0 {
			return
		}
		reached := b.announce(p, batch, true, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
			if len(batch[guildID]) == 0 {
				return nil
			}
			return b.rallyReminderNote(p, batch[guildID], l)
		})
		b.remindByDM(p, batch, reached)
	}
}

// remindByDM sends the reminder by DM to the users on servers the note
// in the thread didn't reach.
func (b *Bot) remindByDM(p lfg.Post, pings map[string][]string, reached map[string]bool) {
	where := b.runName(p)
	at := p.StartAt.Unix()
	for guildID, users := range pings {
		if reached[guildID] {
			continue
		}
		l := b.guildLang(guildID)
		for _, userID := range users {
			b.dm(userID, notice(colorFull, l.T("dm.reminder", where, at, at)+leaderCharLine(p, l)))
		}
	}
}
