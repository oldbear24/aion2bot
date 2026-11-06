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
	b.pingInBatches(p, pings, func(_ string, users []string, _ bool, l i18n.Lang) discordgo.MessageComponent {
		if len(users) == 0 {
			return nil
		}
		return b.rallyReminderNote(p, users, l)
	}, func(guildID string, users []string) {
		b.remindByDM(p, map[string][]string{guildID: users}, nil)
	})
}

// pingInBatches posts notes that ping users in the threads under a post,
// mentionBatch of them per note on each server. note makes the note for a
// server and its users in the batch, who may be none, or returns nil for
// nothing; first is set for the first batch, which goes to every server.
// missed gets the users on servers a note didn't reach, to tell them
// another way. Callers run learnMembers first, as for announce.
func (b *Bot) pingInBatches(p lfg.Post, pings map[string][]string,
	note func(guildID string, users []string, first bool, l i18n.Lang) discordgo.MessageComponent,
	missed func(guildID string, users []string)) {
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
			return note(guildID, batch[guildID], lo == 0, l)
		})
		for guildID, users := range batch {
			if !reached[guildID] {
				missed(guildID, users)
			}
		}
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
