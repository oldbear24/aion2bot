package bot

import (
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const (
	// Clicks within this window are folded into one edit per server.
	syncDelay = time.Second
	// Parallel Discord requests when sending or editing copies of a post.
	fanOut = 5
	// Threads under posts archive after a day without messages. A later
	// ping brings the thread back.
	threadArchiveMinutes = 1440
)

type syncState struct{ dirty bool }

// scheduleSync re-renders every copy of a post shortly. Syncs for the same
// post never overlap, so an older render can't overwrite a newer one.
func (b *Bot) scheduleSync(postID string) {
	b.syncMu.Lock()
	defer b.syncMu.Unlock()
	if st, running := b.syncs[postID]; running {
		st.dirty = true
		return
	}
	st := &syncState{}
	b.syncs[postID] = st
	b.syncWG.Add(1)
	go b.runSync(postID, st)
}

func (b *Bot) runSync(postID string, st *syncState) {
	defer b.syncWG.Done()
	for {
		time.Sleep(syncDelay)
		b.syncPost(postID)

		b.syncMu.Lock()
		if !st.dirty {
			delete(b.syncs, postID)
			b.syncMu.Unlock()
			return
		}
		st.dirty = false
		b.syncMu.Unlock()
	}
}

func (b *Bot) syncPost(postID string) {
	p, ok := b.store.Post(postID)
	if !ok {
		return
	}
	langs := b.guildLangs()
	b.learnMembers(p, p.GuildIDs())
	forEach(p.Messages, func(ref lfg.MessageRef) {
		err := b.edit(ref.ChannelID, ref.MessageID, b.postCard(p, ref.GuildID, ref.PingRoleID, langs.of(ref.GuildID)))
		switch {
		case b.cardLost(err, ref.GuildID):
			log.Printf("forget the card of post %s in guild %s: %v", postID, ref.GuildID, err)
			b.store.RemoveMessage(postID, ref.MessageID)
		case err != nil:
			log.Printf("update post %s in guild %s: %v", postID, ref.GuildID, err)
		}
	})
}

// cardLost reports whether a card that couldn't be edited is lost for good:
// it or its channel no longer exists, or the bot is off the server. Missing
// Access alone doesn't say so, as the bot also gets it while a channel's
// permissions keep it out, and a card it forgets is never updated or deleted
// again.
func (b *Bot) cardLost(err error, guildID string) bool {
	if restCode(err) == discordgo.ErrCodeMissingAccess {
		_, notOnServer := b.s.State.Guild(guildID)
		return notOnServer != nil
	}
	return isGone(err)
}

// broadcast sends a post to every given server that doesn't have it yet and
// returns how many copies were delivered and how many failed. ping is set
// for brand-new posts; copies sent later by /lfg-setup stay quiet. Servers
// whose moderators took the post off, or blocked its leader, are skipped.
func (b *Bot) broadcast(p lfg.Post, guilds []lfg.GuildConfig, ping bool) (sent, failed int) {
	var fresh []lfg.GuildConfig
	var guildIDs []string
	for _, g := range guilds {
		if !p.InGuild(g.GuildID) && b.welcomeIn(p, g.GuildID) {
			fresh = append(fresh, g)
			guildIDs = append(guildIDs, g.GuildID)
		}
	}
	b.learnMembers(p, guildIDs)

	var mu sync.Mutex
	var refs []lfg.MessageRef
	forEach(fresh, func(g lfg.GuildConfig) {
		ref, err := b.deliver(p, g, ping)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			log.Printf("send post %s to guild %s (%s): %v", p.ID, g.GuildID, g.GuildName, err)
			failed++
			return
		}
		refs = append(refs, ref)
	})
	if len(refs) == 0 {
		return 0, failed
	}

	stored, ok := b.store.AddMessages(p.ID, refs)
	// Someone may have clicked a copy that was delivered before the others
	// were recorded; bring every copy up to date.
	if ok && stored.Revision != p.Revision {
		b.scheduleSync(p.ID)
	}
	if ok {
		b.startThreads(stored, refs)
	}
	return len(refs), failed
}

func (b *Bot) deliver(p lfg.Post, g lfg.GuildConfig, ping bool) (lfg.MessageRef, error) {
	role, mentions := "", noMentions
	if ping && g.PingRoleID != "" {
		role = g.PingRoleID
		mentions = &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}, Roles: []string{role}}
	}
	id, err := b.send(g.ChannelID, b.postCard(p, g.GuildID, role, i18n.Parse(g.Lang)), mentions)
	if err != nil {
		return lfg.MessageRef{}, err
	}
	return lfg.MessageRef{GuildID: g.GuildID, ChannelID: g.ChannelID, MessageID: id, PingRoleID: role}, nil
}

func forEach[T any](items []T, fn func(T)) {
	sem := make(chan struct{}, fanOut)
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			fn(it)
		}()
	}
	wg.Wait()
}

// langMap holds the language each server chose for its posts.
type langMap map[string]i18n.Lang

func (m langMap) of(guildID string) i18n.Lang {
	if l, ok := m[guildID]; ok {
		return l
	}
	return i18n.English
}

func (b *Bot) guildLangs() langMap {
	langs := langMap{}
	for _, g := range b.store.Guilds() {
		langs[g.GuildID] = i18n.Parse(g.Lang)
	}
	return langs
}

// deleteEnded removes posts that ended before the cutoff: every copy of the
// card and the thread under it, then the stored post. A post whose
// messages couldn't all be deleted is retried on the next round.
func (b *Bot) deleteEnded(cutoff time.Time) {
	for _, e := range b.store.EndedBefore(cutoff, deleteBatch) {
		if b.deleteMessages(e.ID, e.Messages) == 0 {
			b.store.DeletePost(e.ID)
		}
	}
}

// archiveEnded archives the threads under ended posts that aren't archived
// yet: those that just expired, and those whose threads couldn't all be
// archived before, which are tried again every round.
func (b *Bot) archiveEnded() {
	for _, p := range b.store.ArchiveDue(deleteBatch) {
		b.archiveThreads(p)
	}
}

// archiveThreads archives the threads under an ended post, on every server,
// so they drop out of the channels' thread lists. A message in one brings it
// back, as Discord unarchives a thread on any new message unless a moderator
// locked it, and so does reopening the post, whose note goes to the threads.
// The post counts as archived once none of its threads is left to try again.
func (b *Bot) archiveThreads(p lfg.Post) {
	var threadIDs []string
	for _, ref := range p.Messages {
		if ref.Thread {
			threadIDs = append(threadIDs, ref.MessageID)
		}
	}
	if b.setArchived(p.ID, threadIDs, true) == 0 {
		b.store.MarkArchived(p.ID, p.ClosedAt)
	}
}

// removePost deletes a post right away, when its leader asks for it. It is
// closed first, so a copy that can't be deleted at least shows as ended;
// the 24-hour cleanup retries it later.
func (b *Bot) removePost(postID string) (deleted, failed int) {
	b.store.UpdatePost(postID, func(p *lfg.Post) error {
		if !p.Active() {
			return lfg.ErrNotOpen
		}
		return p.Close(lfg.StatusClosed, time.Now())
	})
	msgs := b.store.AllMessages(postID)
	failed = b.deleteMessages(postID, msgs)
	if failed == 0 {
		b.store.DeletePost(postID)
	} else {
		b.scheduleSync(postID)
	}
	return len(msgs) - failed, failed
}

// deleteMessages deletes the bot's messages for a post and returns how many
// couldn't be deleted. Messages that are already gone count as deleted.
//
// Threads go first and take the pings in them along: the bot may delete a
// thread it started. If Discord refuses anyway, the thread is reopened so
// its pings can be deleted (nothing in an archived thread can be), and
// then archived again.
func (b *Bot) deleteMessages(postID string, msgs []lfg.MessageRef) int {
	var mu sync.Mutex
	failed := 0
	deleted := map[string]bool{}
	var kept []string
	forEach(msgs, func(m lfg.MessageRef) {
		if !m.Thread {
			return
		}
		_, err := b.s.ChannelDelete(m.MessageID)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil || isGone(err):
			deleted[m.MessageID] = true
		case restCode(err) == discordgo.ErrCodeMissingPermissions:
			kept = append(kept, m.MessageID)
		default:
			log.Printf("delete thread %s of post %s: %v", m.MessageID, postID, err)
			failed++
		}
	})
	b.setArchived(postID, kept, false)

	forEach(msgs, func(m lfg.MessageRef) {
		if deleted[m.ChannelID] {
			return
		}
		err := b.s.ChannelMessageDelete(m.ChannelID, m.MessageID)
		// Out of the channel, the bot can't take the message down, and retrying
		// could hold up the cleanup for good. A click retires a card left behind
		// this way (see onPostGone).
		if restCode(err) == discordgo.ErrCodeMissingAccess {
			log.Printf("leave message %s of post %s behind: %v", m.MessageID, postID, err)
		}
		// A ping in a thread that a moderator locked stays out of sight.
		if err != nil && !isGone(err) && restCode(err) != discordgo.ErrCodePerformedOperationOnArchivedThread {
			log.Printf("delete message %s of post %s: %v", m.MessageID, postID, err)
			mu.Lock()
			failed++
			mu.Unlock()
		}
	})

	b.setArchived(postID, kept, true)
	return failed
}

// setArchived archives or reopens threads the bot started, which needs no
// extra permission unless a moderator locked them. It returns how many it
// couldn't change for now. Threads that are gone or archived already don't
// count, and neither do those it may not change: a thread a player started
// is theirs, and a locked one the moderators'.
func (b *Bot) setArchived(postID string, threadIDs []string, archived bool) int {
	var mu sync.Mutex
	failed := 0
	forEach(threadIDs, func(threadID string) {
		_, err := b.s.ChannelEditComplex(threadID, &discordgo.ChannelEdit{Archived: &archived})
		switch restCode(err) {
		case discordgo.ErrCodePerformedOperationOnArchivedThread, discordgo.ErrCodeMissingPermissions, discordgo.ErrCodeThreadIsLocked:
			return
		}
		if err != nil && !isGone(err) {
			log.Printf("set archived=%v on thread %s of post %s: %v", archived, threadID, postID, err)
			mu.Lock()
			failed++
			mu.Unlock()
		}
	})
	return failed
}
