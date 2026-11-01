package bot

import (
	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
)

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
	reached := b.announce(p, pings, true, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.reminderNote(p, guildID, l)
	})
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
