package bot

import (
	"errors"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// Every copy of a post gets a thread on its server, and the bot keeps the
// threads alike: its notes go to all of them, each in its server's
// language, and what players write in one is relayed to the others. The
// leader and everyone who joined are added to the thread on each server
// they're a member of.

const (
	// Discord's limit for the text of a message.
	maxContent = 2000
	// Webhooks post under names of at most 80 characters.
	maxWebhookName = 80
	// After failing to set up a webhook in a channel, the bot relays
	// messages there itself for this long before trying again.
	webhookRetry = 10 * time.Minute
	webhookName  = "AION 2 LFG"
)

var errNoWebhookPermission = errors.New("no Manage Webhooks permission")

// thread returns the thread under one copy of a post, starting it if
// needed. started reports a thread that didn't exist before.
func (b *Bot) thread(p lfg.Post, ref lfg.MessageRef) (threadID string, started bool, err error) {
	if ref.Thread {
		return ref.MessageID, false, nil
	}
	if threadID, err = b.startThread(ref.ChannelID, ref.MessageID, b.threadName(p)); err != nil {
		return "", false, err
	}
	b.store.SetThread(p.ID, ref.MessageID)
	return threadID, true, nil
}

// announce posts a note in the thread under every copy of a post, written
// by note in each server's language. With start set, missing threads are
// started and everyone on the post is added to them; otherwise only the
// existing threads get the note. ping is notified on the home server only,
// so nobody is pinged twice; elsewhere their mention is just a link. It
// reports whether the note reached the home server, so the caller can
// fall back to a DM. Callers run learnMembers first, as note and the
// thread members depend on it.
func (b *Bot) announce(p lfg.Post, home string, ping []string, start bool, note func(guildID string, l i18n.Lang) string) bool {
	langs := b.guildLangs()
	var mu sync.Mutex
	reached := false
	var started []lfg.MessageRef
	forEach(p.Messages, func(ref lfg.MessageRef) {
		if !ref.Thread && !start {
			return
		}
		threadID, fresh, err := b.thread(p, ref)
		var id string
		if err == nil {
			mentions := noMentions
			if ref.GuildID == home {
				mentions = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}, Users: ping}
			}
			id, err = b.send(threadID, []discordgo.MessageComponent{text(note(ref.GuildID, langs.of(ref.GuildID)))}, mentions)
		}
		if err != nil {
			log.Printf("announce on post %s in guild %s: %v", p.ID, ref.GuildID, err)
			return
		}
		b.store.AddReply(p.ID, lfg.MessageRef{GuildID: ref.GuildID, ChannelID: threadID, MessageID: id})
		mu.Lock()
		defer mu.Unlock()
		reached = reached || ref.GuildID == home
		if fresh {
			ref.Thread = true
			started = append(started, ref)
		}
	})
	b.addToThreads(started, p.People())
	return reached
}

// addToThreads adds people to the threads under a post's copies, on each
// server they're a member of, so the thread shows up for them there too.
// Adding someone who is already in a thread changes nothing.
func (b *Bot) addToThreads(threads []lfg.MessageRef, people []lfg.Player) {
	type add struct{ threadID, userID string }
	var adds []add
	for _, ref := range threads {
		if !ref.Thread {
			continue
		}
		for _, pl := range people {
			if b.mentionable(pl.UserID, pl.GuildID, ref.GuildID) {
				adds = append(adds, add{ref.MessageID, pl.UserID})
			}
		}
	}
	forEach(adds, func(a add) {
		err := b.s.ThreadMemberAdd(a.threadID, a.userID)
		// Threads archive after a day without messages; reopen and retry.
		if restCode(err) == discordgo.ErrCodePerformedOperationOnArchivedThread {
			archived := false
			if _, err = b.s.ChannelEditComplex(a.threadID, &discordgo.ChannelEdit{Archived: &archived}); err == nil {
				err = b.s.ThreadMemberAdd(a.threadID, a.userID)
			}
		}
		if err != nil && restCode(err) != discordgo.ErrCodeUnknownMember && !isGone(err) {
			log.Printf("add %s to thread %s: %v", a.userID, a.threadID, err)
		}
	})
}

// relayable reports whether a message was written by a player: not by a
// bot or a webhook (so never one of the bot's own relays), and not a
// system message such as "added to the thread".
func relayable(m *discordgo.Message) bool {
	return m.GuildID != "" && m.Author != nil && !m.Author.Bot && m.WebhookID == "" &&
		(m.Type == discordgo.MessageTypeDefault || m.Type == discordgo.MessageTypeReply)
}

// relayLock keeps the messages of one thread in order while they are
// relayed, edited or deleted. Threads share a few locks.
func (b *Bot) relayLock(threadID string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(threadID))
	return &b.relayMu[h.Sum32()%uint32(len(b.relayMu))]
}

// onMessageCreate relays what a player writes in a post's thread to the
// threads on the other servers.
func (b *Bot) onMessageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	defer guard("relaying a message")
	if !relayable(m.Message) {
		return
	}
	p, ok := b.store.PostByThread(m.ChannelID)
	if !ok {
		return
	}
	b.members.set(m.GuildID, m.Author.ID, true, time.Now())
	mu := b.relayLock(m.ChannelID)
	mu.Lock()
	defer mu.Unlock()
	b.relay(p, m.Message)
}

func (b *Bot) relay(p lfg.Post, m *discordgo.Message) {
	content := b.relayContent(m)
	if content == "" {
		return
	}
	// A thread a player started on the card counts as the post's thread.
	if ref, ok := p.MessageIn(m.GuildID); ok && !ref.Thread {
		b.store.SetThread(p.ID, ref.MessageID)
	}
	from := b.authorOf(m)

	var mu sync.Mutex
	var copies []lfg.Relay
	var started []lfg.MessageRef
	forEach(p.Messages, func(ref lfg.MessageRef) {
		if ref.GuildID == m.GuildID {
			return
		}
		threadID, fresh, err := b.thread(p, ref)
		var c lfg.Relay
		if err == nil {
			c, err = b.relayTo(ref.ChannelID, threadID, from, content)
		}
		if err != nil {
			log.Printf("relay message %s of post %s to guild %s: %v", m.ID, p.ID, ref.GuildID, err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		copies = append(copies, c)
		if fresh {
			ref.Thread = true
			started = append(started, ref)
		}
	})
	b.store.AddRelays(p.ID, m.ID, copies)
	if len(started) > 0 {
		var guildIDs []string
		for _, ref := range started {
			guildIDs = append(guildIDs, ref.GuildID)
		}
		b.learnMembers(p, guildIDs)
		b.addToThreads(started, p.People())
	}
}

// author is who wrote a relayed message, as the other servers see it.
type author struct{ name, server, avatar string }

func (b *Bot) authorOf(m *discordgo.Message) author {
	name := m.Author.DisplayName()
	if m.Member != nil && m.Member.Nick != "" {
		name = m.Member.Nick
	}
	return author{name: name, server: b.guildName(m.GuildID), avatar: m.Author.AvatarURL("128")}
}

// webhookName is "Name · Server".
func (a author) webhookName() string {
	if a.server == "" {
		return truncate(a.name, maxWebhookName)
	}
	return truncate(a.name+" · "+a.server, maxWebhookName)
}

// prefixed is a message the bot relays itself, led by its author.
func (a author) prefixed(content string) string {
	head := "**" + escape(a.name) + "**"
	if a.server != "" {
		head += " · *" + escape(a.server) + "*"
	}
	return truncate(head+": "+content, maxContent)
}

// relayContent is a player's message as the other servers see it: mentions
// written as names, which other servers couldn't resolve, and attachments
// as links, which Discord shows inline. Stickers are left out.
func (b *Bot) relayContent(m *discordgo.Message) string {
	content, _ := m.ContentWithMoreMentionsReplaced(b.s)
	var links []string
	for _, a := range m.Attachments {
		links = append(links, a.URL)
	}
	tail := strings.Join(links, "\n")
	room := maxContent - len([]rune(tail)) - 1
	content = strings.TrimSpace(content)
	if room <= 0 {
		return truncate(tail, maxContent)
	}
	if content != "" {
		content = truncate(content, room)
	}
	return strings.TrimSpace(content + "\n" + tail)
}

// relayTo posts a relayed message in a thread under a channel: through the
// channel's webhook, under the author's name and avatar, or by the bot
// itself where it has no webhook.
func (b *Bot) relayTo(channelID, threadID string, from author, content string) (lfg.Relay, error) {
	if w, ok := b.webhook(channelID); ok {
		msg, err := b.s.WebhookThreadExecute(w.ID, w.Token, true, threadID, &discordgo.WebhookParams{
			Content: content, Username: from.webhookName(), AvatarURL: from.avatar, AllowedMentions: noMentions,
		})
		if err == nil {
			return lfg.Relay{ChannelID: threadID, MessageID: msg.ID, Webhook: w}, nil
		}
		switch restCode(err) {
		case discordgo.ErrCodeUnknownWebhook, discordgo.ErrCodeInvalidWebhookTokenProvided:
			// Someone deleted it; the next message sets up a new one.
			b.store.RemoveWebhook(channelID)
		default:
			log.Printf("relay through webhook into thread %s: %v", threadID, err)
		}
	}
	msg, err := b.s.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{
		Content: from.prefixed(content), AllowedMentions: noMentions,
	})
	if err != nil {
		return lfg.Relay{}, err
	}
	return lfg.Relay{ChannelID: threadID, MessageID: msg.ID}, nil
}

// webhook returns the bot's webhook in a channel, setting one up the first
// time. That needs Manage Webhooks; without it the bot relays messages
// itself.
func (b *Bot) webhook(channelID string) (lfg.Webhook, bool) {
	if w, ok := b.store.Webhook(channelID); ok {
		return w, true
	}
	b.hookMu.Lock()
	defer b.hookMu.Unlock()
	if w, ok := b.store.Webhook(channelID); ok {
		return w, true
	}
	if time.Since(b.hookFailed[channelID]) < webhookRetry {
		return lfg.Webhook{}, false
	}
	w, err := b.setUpWebhook(channelID)
	if err != nil {
		if !errors.Is(err, errNoWebhookPermission) {
			log.Printf("set up webhook in %s: %v", channelID, err)
		}
		b.hookFailed[channelID] = time.Now()
		return lfg.Webhook{}, false
	}
	b.store.SetWebhook(channelID, w)
	return w, true
}

func (b *Bot) setUpWebhook(channelID string) (lfg.Webhook, error) {
	botID := b.s.State.User.ID
	perms, err := b.s.UserChannelPermissions(botID, channelID)
	if err != nil {
		return lfg.Webhook{}, err
	}
	if perms&discordgo.PermissionManageWebhooks == 0 {
		return lfg.Webhook{}, errNoWebhookPermission
	}
	// Reuse the one made before, e.g. if the database was reset.
	hooks, err := b.s.ChannelWebhooks(channelID)
	if err != nil {
		return lfg.Webhook{}, err
	}
	for _, h := range hooks {
		if h.ApplicationID == botID && h.Token != "" {
			return lfg.Webhook{ID: h.ID, Token: h.Token}, nil
		}
	}
	h, err := b.s.WebhookCreate(channelID, webhookName, "")
	if err != nil {
		return lfg.Webhook{}, err
	}
	return lfg.Webhook{ID: h.ID, Token: h.Token}, nil
}

// webhookEdit changes a relayed message's text. discordgo's own webhook
// edit can't name the thread the message is in.
type webhookEdit struct {
	Content         string                            `json:"content"`
	AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions,omitempty"`
}

// webhookMessage is the endpoint of a message a webhook posted in a thread.
func webhookMessage(c lfg.Relay) string {
	return discordgo.EndpointWebhookMessage(c.Webhook.ID, c.Webhook.Token, c.MessageID) + "?thread_id=" + c.ChannelID
}

// onMessageUpdate carries a player's edit over to the relayed copies.
func (b *Bot) onMessageUpdate(_ *discordgo.Session, m *discordgo.MessageUpdate) {
	defer guard("relaying an edit")
	if m.EditedTimestamp == nil || !relayable(m.Message) {
		return
	}
	mu := b.relayLock(m.ChannelID)
	mu.Lock()
	defer mu.Unlock()
	copies := b.store.Relays(m.ID)
	content := b.relayContent(m.Message)
	if len(copies) == 0 || content == "" {
		return
	}
	from := b.authorOf(m.Message)
	forEach(copies, func(c lfg.Relay) {
		var err error
		if c.Webhook.ID != "" {
			endpoint := webhookMessage(c)
			_, err = b.s.RequestWithBucketID("PATCH", endpoint, webhookEdit{Content: content, AllowedMentions: noMentions},
				discordgo.EndpointWebhookToken("", ""))
		} else {
			text := from.prefixed(content)
			_, err = b.s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: c.MessageID, Channel: c.ChannelID,
				Content: &text, AllowedMentions: noMentions})
		}
		if err != nil && !isGone(err) && restCode(err) != discordgo.ErrCodePerformedOperationOnArchivedThread {
			log.Printf("relay edit of message %s into thread %s: %v", m.ID, c.ChannelID, err)
		}
	})
}

func (b *Bot) onMessageDelete(_ *discordgo.Session, m *discordgo.MessageDelete) {
	defer guard("relaying a delete")
	b.unrelay(m.ChannelID, m.ID)
}

func (b *Bot) onMessageDeleteBulk(_ *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	defer guard("relaying a bulk delete")
	for _, id := range m.Messages {
		b.unrelay(m.ChannelID, id)
	}
}

// unrelay deletes the copies of a message deleted from a post's thread, so
// what a moderator removes on one server disappears from all of them.
func (b *Bot) unrelay(channelID, messageID string) {
	mu := b.relayLock(channelID)
	mu.Lock()
	defer mu.Unlock()
	copies := b.store.Relays(messageID)
	if len(copies) == 0 {
		return
	}
	forEach(copies, func(c lfg.Relay) {
		var err error
		if c.Webhook.ID != "" {
			endpoint := webhookMessage(c)
			_, err = b.s.RequestWithBucketID("DELETE", endpoint, nil, discordgo.EndpointWebhookToken("", ""))
		} else {
			err = b.s.ChannelMessageDelete(c.ChannelID, c.MessageID)
		}
		if err != nil && !isGone(err) && restCode(err) != discordgo.ErrCodePerformedOperationOnArchivedThread {
			log.Printf("relay delete of message %s into thread %s: %v", messageID, c.ChannelID, err)
		}
	})
	b.store.RemoveRelays(messageID)
}
