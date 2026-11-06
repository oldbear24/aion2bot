package bot

import (
	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// The bot pings each player about their runs on one server: the one they
// posted or joined from, in the thread under the post there. A player who
// picked DMs in /profile gets those pings by DM instead; the notes still go
// to every thread, mentioning them without a ping. A DM that doesn't go
// through falls back to the ping in the thread, and a ping the thread can't
// take, as the post has no copy or thread on that server, to a DM.

// mentionBatch is how many users one note pings; Discord notifies at most
// 100 users per message.
const mentionBatch = 50

// wantsDM reports whether a user picked DMs for their pings in /profile.
func (b *Bot) wantsDM(userID string) bool {
	prof, ok := b.store.Profile(userID)
	return ok && prof.ByDM
}

// pingOrDM tells users about something that happened on a post: note goes
// to the thread under every copy of it, as with announce, pinging each user
// in pings on the server it maps them to, unless they get it by DM. dm
// sends one of them the news by DM, in that server's language, and reports
// whether it went through. Callers run learnMembers first, as for announce.
func (b *Bot) pingOrDM(p lfg.Post, pings map[string][]string,
	note func(guildID string, l i18n.Lang) discordgo.MessageComponent, dm func(guildID, userID string) bool) {
	pings, tried := b.sendDMs(pings, dm)
	b.dmMissed(pings, b.announce(p, pings, true, note), tried, dm)
}

// pingInBatches is pingOrDM for notes that name the users they ping, so a
// note pings at most mentionBatch of them on each server. note makes the
// note for a server and its users in the batch, who may be none, or returns
// nil for nothing; first is set for the first batch, which goes to every
// server.
func (b *Bot) pingInBatches(p lfg.Post, pings map[string][]string,
	note func(guildID string, users []string, first bool, l i18n.Lang) discordgo.MessageComponent,
	dm func(guildID, userID string) bool) {
	pings, tried := b.sendDMs(pings, dm)
	longest := 0
	for _, users := range pings {
		longest = max(longest, len(users))
	}
	for lo := 0; lo == 0 || lo < longest; lo += mentionBatch {
		batch := map[string][]string{}
		for guildID, users := range pings {
			if lo < len(users) {
				batch[guildID] = users[lo:min(len(users), lo+mentionBatch)]
			}
		}
		reached := b.announce(p, batch, true, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
			return note(guildID, batch[guildID], lo == 0, l)
		})
		b.dmMissed(batch, reached, tried, dm)
	}
}

// sendDMs sends dm to the users among pings who want their pings by DM,
// and returns the pings left for the threads: the other users, and those
// whose DM didn't go through. tried holds the users it sent one to.
func (b *Bot) sendDMs(pings map[string][]string, dm func(guildID, userID string) bool) (left map[string][]string, tried map[string]bool) {
	type target struct {
		guildID, userID string
		sent            bool
	}
	var wanted []*target
	for guildID, users := range pings {
		for _, id := range users {
			if b.wantsDM(id) {
				wanted = append(wanted, &target{guildID: guildID, userID: id})
			}
		}
	}
	forEach(wanted, func(t *target) { t.sent = dm(t.guildID, t.userID) })

	tried, sent := map[string]bool{}, map[string]bool{}
	for _, t := range wanted {
		tried[t.userID], sent[t.userID] = true, t.sent
	}
	left = map[string][]string{}
	for guildID, users := range pings {
		for _, id := range users {
			if !sent[id] {
				left[guildID] = append(left[guildID], id)
			}
		}
	}
	return left, tried
}

// dmMissed sends dm to the users of pings on servers the note didn't reach,
// except those it was tried for already.
func (b *Bot) dmMissed(pings map[string][]string, reached, tried map[string]bool, dm func(guildID, userID string) bool) {
	type target struct{ guildID, userID string }
	var missed []target
	for guildID, users := range pings {
		if reached[guildID] {
			continue
		}
		for _, id := range users {
			if !tried[id] {
				missed = append(missed, target{guildID, id})
			}
		}
	}
	forEach(missed, func(t target) { dm(t.guildID, t.userID) })
}
