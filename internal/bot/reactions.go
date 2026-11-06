package bot

import (
	"log"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/lfg"
)

// Reactions follow relayed messages too. Where someone reacts on one
// server, the bot reacts with the same emoji on the message's copies on
// the others (and on the message itself, for a reaction on a copy). The
// bot counts once however many reacted elsewhere, so each server sees its
// own players' reactions, plus the bot's for everyone else. It takes its
// reaction back once nobody on the other servers has that one left.
//
// A moderator who clears a message's reactions, or one emoji, clears what
// the bot added on the other servers too. What players added there is
// theirs, and stays.

// codeTooManyReactions is Discord's answer once a message has 20
// different reactions.
const codeTooManyReactions = 30010

func (b *Bot) onReactionAdd(_ *discordgo.Session, r *discordgo.MessageReactionAdd) {
	defer guard("mirroring a reaction")
	if r.MessageReaction == nil || r.Member != nil && r.Member.User != nil && r.Member.User.Bot {
		return
	}
	b.mirrorReaction(r.MessageReaction, true)
}

func (b *Bot) onReactionRemove(_ *discordgo.Session, r *discordgo.MessageReactionRemove) {
	defer guard("mirroring a reaction")
	if r.MessageReaction != nil {
		b.mirrorReaction(r.MessageReaction, false)
	}
}

func (b *Bot) onReactionRemoveAll(_ *discordgo.Session, r *discordgo.MessageReactionRemoveAll) {
	defer guard("clearing mirrored reactions")
	if r.MessageReaction != nil {
		b.clearReactions(r.MessageID, "")
	}
}

func (b *Bot) onReactionRemoveEmoji(_ *discordgo.Session, r *discordgo.MessageReactionRemoveEmoji) {
	defer guard("clearing mirrored reactions")
	if r.MessageReaction != nil {
		b.clearReactions(r.MessageID, r.Emoji.APIName())
	}
}

// me is the bot's own user ID.
func (b *Bot) me() string {
	if b.s.State != nil && b.s.State.User != nil {
		return b.s.State.User.ID
	}
	return ""
}

// mirrorReaction records a player's reaction on a relayed message or one
// of its copies, and brings the bot's reactions on the others in line.
func (b *Bot) mirrorReaction(r *discordgo.MessageReaction, added bool) {
	me := b.me()
	if r.GuildID == "" || r.UserID == "" || me == "" || r.UserID == me {
		return
	}
	src, copies, ok := b.store.RelayGroup(r.MessageID)
	if !ok {
		return
	}
	mu := b.relayLock(src.MessageID)
	mu.Lock()
	defer mu.Unlock()
	reaction := lfg.Reaction{MessageID: r.MessageID, Emoji: r.Emoji.APIName(), UserID: r.UserID}
	if added {
		b.store.AddReaction(src, reaction)
	} else {
		b.store.RemoveReaction(reaction)
	}
	b.syncReaction(src, copies, reaction.Emoji)
}

// relayGroup is a relayed message and its copies, each with its server.
// Messages relayed by earlier versions don't say where the message itself
// is, so only their copies count.
func relayGroup(src lfg.Relayed, copies []lfg.Relay) []lfg.Relay {
	var all []lfg.Relay
	if src.ChannelID != "" {
		all = append(all, lfg.Relay{GuildID: src.GuildID, ChannelID: src.ChannelID, MessageID: src.MessageID})
	}
	return append(all, copies...)
}

// syncReaction has the bot react with an emoji on every message of the
// group that a player reacted with it on another one of, as long as that
// player isn't blocked on the message's server, and take its reaction
// back from the rest.
func (b *Bot) syncReaction(src lfg.Relayed, copies []lfg.Relay, emoji string) {
	me := b.me()
	reactions := b.store.Reactions(src.MessageID, emoji)
	forEach(relayGroup(src, copies), func(c lfg.Relay) {
		want, have := false, false
		for _, r := range reactions {
			switch {
			case r.UserID == me:
				have = have || r.MessageID == c.MessageID
			case r.MessageID != c.MessageID && !want:
				want = !b.store.Blocked(r.UserID, c.GuildID)
			}
		}
		mine := lfg.Reaction{MessageID: c.MessageID, Emoji: emoji, UserID: me}
		switch {
		case want && !have:
			if err := b.s.MessageReactionAdd(c.ChannelID, c.MessageID, emoji); err != nil {
				logReactionError("add reaction", c, err)
				return
			}
			b.store.AddReaction(src, mine)
		case !want && have:
			if err := b.s.MessageReactionRemove(c.ChannelID, c.MessageID, emoji, "@me"); err != nil && !isGone(err) {
				logReactionError("take back reaction", c, err)
				return
			}
			b.store.RemoveReaction(mine)
		}
	})
}

// clearReactions follows a moderator clearing the reactions on a relayed
// message or a copy, or only those with one emoji: the bot takes back what
// it added for them on the other servers, and forgets them.
func (b *Bot) clearReactions(messageID, emoji string) {
	me := b.me()
	src, copies, ok := b.store.RelayGroup(messageID)
	if !ok || me == "" {
		return
	}
	mu := b.relayLock(src.MessageID)
	mu.Lock()
	defer mu.Unlock()
	where := map[string]lfg.Relay{}
	for _, c := range relayGroup(src, copies) {
		where[c.MessageID] = c
	}
	type mirror struct {
		at    lfg.Relay
		emoji string
	}
	var mine []mirror
	for _, r := range b.store.Reactions(src.MessageID, emoji) {
		if c, ok := where[r.MessageID]; ok && r.UserID == me && r.MessageID != messageID {
			mine = append(mine, mirror{c, r.Emoji})
		}
	}
	b.store.ForgetReactions(src.MessageID, emoji)
	forEach(mine, func(m mirror) {
		if err := b.s.MessageReactionRemove(m.at.ChannelID, m.at.MessageID, m.emoji, "@me"); err != nil && !isGone(err) {
			logReactionError("take back reaction", m.at, err)
		}
	})
}

// logReactionError logs what keeps the bot from reacting, except what it
// can't help: an emoji from a server it isn't on, a channel it may not
// react in, a closed thread, a message gone, or one with all the
// reactions Discord allows.
func logReactionError(what string, c lfg.Relay, err error) {
	switch restCode(err) {
	case discordgo.ErrCodeUnknownEmoji, discordgo.ErrCodeMissingPermissions, discordgo.ErrCodeReactionBlocked,
		discordgo.ErrCodePerformedOperationOnArchivedThread, codeTooManyReactions:
		return
	}
	if !isGone(err) {
		log.Printf("%s on message %s in thread %s: %v", what, c.MessageID, c.ChannelID, err)
	}
}
