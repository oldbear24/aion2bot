package bot

import (
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/lfg"
)

// Discord shows a mention as a clickable name only to members of the
// server the message is on; elsewhere it reads "@unknown-user". Posts are
// copied to linked servers, so the bot finds out where the leader and the
// players are members and links them on every one of those servers.

// memberTTL is how long the bot trusts what it found out about a member.
const memberTTL = time.Hour

type memberKey struct{ guildID, userID string }

type memberEntry struct {
	member bool
	at     time.Time
}

// memberCache remembers who is a member of which server. The zero value is
// empty and ready to use.
type memberCache struct {
	mu      sync.Mutex
	entries map[memberKey]memberEntry
}

func (c *memberCache) get(guildID, userID string, now time.Time) (member, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[memberKey{guildID, userID}]
	if !ok || now.Sub(e.at) > memberTTL {
		return false, false
	}
	return e.member, true
}

func (c *memberCache) set(guildID, userID string, member bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[memberKey]memberEntry{}
	}
	c.entries[memberKey{guildID, userID}] = memberEntry{member, now}
}

func (c *memberCache) dropStale(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.Sub(e.at) > memberTTL {
			delete(c.entries, k)
		}
	}
}

// mentionable reports whether a user can be linked with a mention on a
// server: it's where they used the bot, or the bot found them among the
// server's members.
func (b *Bot) mentionable(userID, homeGuildID, guildID string) bool {
	if guildID == "" {
		return false
	}
	if guildID == homeGuildID {
		return true
	}
	member, _ := b.members.get(guildID, userID, time.Now())
	return member
}

// learnMembers finds out which of a post's people are members of the given
// servers, asking Discord only about those the cache doesn't know. Cards
// and pings only read the cache, so they never wait on Discord; this runs
// before them.
func (b *Bot) learnMembers(p lfg.Post, guildIDs []string) {
	now := time.Now()
	var unknown []memberKey
	for _, guildID := range guildIDs {
		for _, pl := range p.People() {
			if pl.GuildID == guildID {
				continue
			}
			if _, known := b.members.get(guildID, pl.UserID, now); !known {
				unknown = append(unknown, memberKey{guildID, pl.UserID})
			}
		}
	}
	forEach(unknown, func(k memberKey) {
		_, err := b.s.GuildMember(k.guildID, k.userID)
		switch code := restCode(err); {
		case err == nil:
			b.members.set(k.guildID, k.userID, true, time.Now())
		case code == discordgo.ErrCodeUnknownMember || code == discordgo.ErrCodeUnknownUser:
			b.members.set(k.guildID, k.userID, false, time.Now())
		default:
			// Until the next try, the user shows by name there.
			log.Printf("check whether %s is on server %s: %v", k.userID, k.guildID, err)
		}
	})
}
