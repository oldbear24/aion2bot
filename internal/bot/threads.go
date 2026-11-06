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

// announce posts a note in the thread under every copy of a post, made
// by note in each server's language (see notes.go). With start set, missing threads are
// started and everyone on the post is added to them; otherwise only the
// existing threads get the note. pings maps a server to the users notified
// there: each user's home server only, so nobody is pinged twice;
// elsewhere their mention is just a link. It returns the servers the note
// reached, so the caller can fall back to a DM. Callers run learnMembers
// first, as note and the thread members depend on it. A server for which
// note returns nil gets nothing.
func (b *Bot) announce(p lfg.Post, pings map[string][]string, start bool, note func(guildID string, l i18n.Lang) discordgo.MessageComponent) map[string]bool {
	langs := b.guildLangs()
	var mu sync.Mutex
	reached := map[string]bool{}
	var started []lfg.MessageRef
	forEach(p.Messages, func(ref lfg.MessageRef) {
		if !ref.Thread && !start {
			return
		}
		card := note(ref.GuildID, langs.of(ref.GuildID))
		if card == nil {
			return
		}
		threadID, fresh, err := b.thread(p, ref)
		var id string
		if err == nil {
			mentions := noMentions
			if users := pings[ref.GuildID]; len(users) > 0 {
				mentions = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}, Users: users}
			}
			id, err = b.send(threadID, []discordgo.MessageComponent{card}, mentions)
		}
		if err != nil {
			log.Printf("announce on post %s in guild %s: %v", p.ID, ref.GuildID, err)
			return
		}
		b.store.AddReply(p.ID, lfg.MessageRef{GuildID: ref.GuildID, ChannelID: threadID, MessageID: id})
		mu.Lock()
		defer mu.Unlock()
		reached[ref.GuildID] = true
		if fresh {
			ref.Thread = true
			started = append(started, ref)
		}
	})
	b.addToThreads(started, p.People())
	return reached
}

// startThreads starts the thread under copies of a post as they go up, so
// every post has one from the start, and adds everyone on the post to
// them. Without thread permissions on a server, its thread is started on
// demand later, or its pings go by DM.
func (b *Bot) startThreads(p lfg.Post, refs []lfg.MessageRef) {
	var mu sync.Mutex
	var started []lfg.MessageRef
	forEach(refs, func(ref lfg.MessageRef) {
		if _, _, err := b.thread(p, ref); err != nil {
			if restCode(err) != discordgo.ErrCodeMissingPermissions {
				log.Printf("start thread for post %s in guild %s: %v", p.ID, ref.GuildID, err)
			}
			return
		}
		ref.Thread = true
		mu.Lock()
		defer mu.Unlock()
		started = append(started, ref)
	})
	b.addToThreads(started, p.People())
}

// renameThreads names the threads under a post after its dungeon and mode
// again, reopening archived ones. Discord allows two renames per thread
// in ten minutes and makes a third wait, so callers run this on its own.
func (b *Bot) renameThreads(p lfg.Post) {
	defer guard("renaming threads")
	name, open := b.threadName(p), false
	forEach(p.Messages, func(ref lfg.MessageRef) {
		if !ref.Thread {
			return
		}
		_, err := b.s.ChannelEditComplex(ref.MessageID, &discordgo.ChannelEdit{Name: name, Archived: &open})
		// A thread a player started is theirs to name.
		if err != nil && !isGone(err) && restCode(err) != discordgo.ErrCodeMissingPermissions {
			log.Printf("rename thread %s of post %s: %v", ref.MessageID, p.ID, err)
		}
	})
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
	to := b.repliedTo(m)
	room := maxContent
	if to != nil {
		room -= maxQuote + 1
	}
	content := b.relayBody(m, room)
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
		// Nothing from a player blocked there, or in the whole network.
		if ref.GuildID == m.GuildID || b.store.Blocked(m.Author.ID, ref.GuildID) {
			return
		}
		threadID, fresh, err := b.thread(p, ref)
		var c lfg.Relay
		if err == nil {
			quote, replyID := b.quoteIn(to, ref.GuildID, threadID)
			c, err = b.relayTo(ref.ChannelID, threadID, from, content, quote, replyID)
		}
		if err != nil {
			log.Printf("relay message %s of post %s to guild %s: %v", m.ID, p.ID, ref.GuildID, err)
			return
		}
		c.GuildID = ref.GuildID
		mu.Lock()
		defer mu.Unlock()
		copies = append(copies, c)
		if fresh {
			ref.Thread = true
			started = append(started, ref)
		}
	})
	b.store.AddRelays(lfg.Relayed{PostID: p.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, MessageID: m.ID,
		AuthorID: m.Author.ID, Author: from.name}, copies)
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
	return author{name: authorName(m), server: b.guildName(m.GuildID), avatar: m.Author.AvatarURL("128")}
}

// authorName is the name a message's author goes by on its server.
func authorName(m *discordgo.Message) string {
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	return m.Author.DisplayName()
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
// written as names, which other servers couldn't resolve, attachments and
// stickers as links, which Discord shows inline, and a forwarded message
// as a quote.
func (b *Bot) relayContent(m *discordgo.Message) string { return b.relayBody(m, maxContent) }

// relayBody is relayContent in at most room characters. A long text is cut
// short, so the attachments still fit.
func (b *Bot) relayBody(m *discordgo.Message, room int) string {
	content, _ := m.ContentWithMoreMentionsReplaced(b.s)
	content = strings.TrimSpace(content)
	links := mediaLinks(m)
	if fw := forwarded(m); fw != nil {
		text, _ := fw.ContentWithMoreMentionsReplaced(b.s)
		if text = strings.TrimSpace(text); text != "" {
			content = strings.TrimSpace(content + "\n" + quote(text))
		}
		links = append(links, mediaLinks(fw)...)
	}
	tail := strings.Join(links, "\n")
	left := room - len([]rune(tail)) - 1
	if left <= 0 {
		return truncate(tail, room)
	}
	if content != "" {
		content = truncate(content, left)
	}
	return strings.TrimSpace(content + "\n" + tail)
}

// forwarded is the message a player forwarded, if they did.
func forwarded(m *discordgo.Message) *discordgo.Message {
	if m.MessageReference == nil || m.MessageReference.Type != discordgo.MessageReferenceTypeForward {
		return nil
	}
	for _, snap := range m.MessageSnapshots {
		if snap.Message != nil {
			fw := *snap.Message
			if fw.ChannelID == "" {
				fw.ChannelID = m.ChannelID
			}
			return &fw
		}
	}
	return nil
}

// mediaLinks are a message's attachments and stickers as links. Discord
// draws animated stickers itself and has no picture of them, so those
// come as their name.
func mediaLinks(m *discordgo.Message) []string {
	var links []string
	for _, a := range m.Attachments {
		links = append(links, a.URL)
	}
	for _, st := range m.StickerItems {
		switch st.FormatType {
		case discordgo.StickerFormatTypePNG, discordgo.StickerFormatTypeAPNG:
			links = append(links, "https://media.discordapp.net/stickers/"+st.ID+".png?size=160")
		case discordgo.StickerFormatTypeGIF:
			links = append(links, "https://media.discordapp.net/stickers/"+st.ID+".gif?size=160")
		default:
			links = append(links, "🏷️ *"+escape(st.Name)+"*")
		}
	}
	return links
}

// withQuote puts the line that shows what a reply answers before it.
func withQuote(quote, text string) string {
	if quote == "" {
		return text
	}
	return truncate(quote+"\n"+text, maxContent)
}

// relayTo posts a relayed message in a thread under a channel: through the
// channel's webhook, under the author's name and avatar, or by the bot
// itself where it has no webhook. A reply starts with quote, the line that
// shows what it answers. The bot itself replies to replyID instead, the
// copy of that message in the thread, when there is one: webhooks can't.
func (b *Bot) relayTo(channelID, threadID string, from author, content, quote, replyID string) (lfg.Relay, error) {
	if w, ok := b.webhook(channelID); ok {
		msg, err := b.s.WebhookThreadExecute(w.ID, w.Token, true, threadID, &discordgo.WebhookParams{
			Content: withQuote(quote, content), Username: from.webhookName(), AvatarURL: from.avatar, AllowedMentions: noMentions,
		})
		if err == nil {
			return lfg.Relay{ChannelID: threadID, MessageID: msg.ID, Webhook: w, Quote: quote}, nil
		}
		switch restCode(err) {
		case discordgo.ErrCodeUnknownWebhook, discordgo.ErrCodeInvalidWebhookTokenProvided:
			// Someone deleted it; the next message sets up a new one.
			b.store.RemoveWebhook(channelID)
		default:
			log.Printf("relay through webhook into thread %s: %v", threadID, err)
		}
	}
	send := &discordgo.MessageSend{Content: withQuote(quote, from.prefixed(content)), AllowedMentions: noMentions}
	if replyID != "" {
		quote = ""
		failIfGone := false
		send.Content = from.prefixed(content)
		send.Reference = &discordgo.MessageReference{MessageID: replyID, ChannelID: threadID, FailIfNotExists: &failIfGone}
	}
	msg, err := b.s.ChannelMessageSendComplex(threadID, send)
	if err != nil {
		return lfg.Relay{}, err
	}
	return lfg.Relay{ChannelID: threadID, MessageID: msg.ID, Quote: quote}, nil
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
	room := maxContent
	for _, c := range copies {
		if c.Quote != "" {
			room = maxContent - maxQuote - 1
		}
	}
	content := b.relayBody(m.Message, room)
	if len(copies) == 0 || content == "" {
		return
	}
	from := b.authorOf(m.Message)
	forEach(copies, func(c lfg.Relay) {
		var err error
		if c.Webhook.ID != "" {
			endpoint := webhookMessage(c)
			_, err = b.s.RequestWithBucketID("PATCH", endpoint, webhookEdit{Content: withQuote(c.Quote, content), AllowedMentions: noMentions},
				discordgo.EndpointWebhookToken("", ""))
		} else {
			text := withQuote(c.Quote, from.prefixed(content))
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
