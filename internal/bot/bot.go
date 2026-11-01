// Package bot connects the LFG posts to Discord: slash commands, the
// create form, and the post cards mirrored to every configured server.
package bot

import (
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"

	"aion2bot/internal/config"
	appemoji "aion2bot/internal/emoji"
	"aion2bot/internal/lfg"
)

const (
	maintenanceEvery = time.Minute
	// Ended (closed or expired) posts are deleted from every channel, along
	// with the bot's replies under them, this long after they end.
	keepEndedPosts = 24 * time.Hour
	// Posts deleted per maintenance round, to stay clear of rate limits.
	deleteBatch = 20
	// The stats look back at most this far.
	keepRuns = 90 * 24 * time.Hour
)

type Bot struct {
	s        *discordgo.Session
	game     *config.Game
	store    *lfg.Store
	devGuild string

	syncMu sync.Mutex
	syncs  map[string]*syncState
	syncWG sync.WaitGroup

	draftMu sync.Mutex
	drafts  map[string]*draft

	// members tells where the people on posts can be linked with mentions.
	members memberCache

	// icons replaces the emojis in messages with the bot's own icons. It is
	// set before connecting and never changes after.
	icons iconSet

	// relayMu keeps each thread's relayed messages in order (see relayLock).
	relayMu [16]sync.Mutex
	// hookMu guards setting up webhooks; hookFailed remembers when it last
	// failed in a channel.
	hookMu     sync.Mutex
	hookFailed map[string]time.Time

	// commandIDs lets messages show clickable command mentions.
	cmdMu      sync.RWMutex
	commandIDs map[string]string

	stop chan struct{}
	done chan struct{}
}

func New(token string, game *config.Game, store *lfg.Store, devGuild string) (*Bot, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	// Thread messages are relayed between servers, which needs their text.
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	b := &Bot{
		s:          s,
		game:       game,
		store:      store,
		devGuild:   devGuild,
		syncs:      map[string]*syncState{},
		drafts:     map[string]*draft{},
		hookFailed: map[string]time.Time{},

		commandIDs: map[string]string{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	s.AddHandler(b.onInteraction)
	s.AddHandler(b.onGuildDelete)
	s.AddHandler(b.onMessageCreate)
	s.AddHandler(b.onMessageUpdate)
	s.AddHandler(b.onMessageDelete)
	s.AddHandler(b.onMessageDeleteBulk)
	return b, nil
}

// closeDisallowedIntents is the gateway's answer to a privileged intent
// that isn't enabled in the Developer Portal.
const closeDisallowedIntents = 4014

func (b *Bot) Start() error {
	// The icons are set before connecting, so no interaction reads the game
	// data while it changes.
	if me, err := b.s.User("@me"); err != nil {
		return fmt.Errorf("connect to Discord: %w", err)
	} else if icons, err := appemoji.Sync(b.s, me.ID); err != nil {
		log.Printf("upload the icons, using emojis instead: %v", err)
	} else {
		b.game.UseIcons(icons)
		b.icons = newIconSet(icons)
	}

	err := b.s.Open()
	if websocket.IsCloseError(err, closeDisallowedIntents) {
		log.Print("The Message Content intent is off for this bot, so messages in threads aren't relayed between servers. " +
			"Turn on Message Content Intent under Bot in the Discord Developer Portal and restart.")
		b.s.Identify.Intents = discordgo.IntentsGuilds
		err = b.s.Open()
	}
	if err != nil {
		return fmt.Errorf("connect to Discord: %w", err)
	}
	if err := b.registerCommands(); err != nil {
		b.s.Close()
		return err
	}
	// Bring the open posts up to date with this version's layout.
	for _, p := range b.store.ActivePosts() {
		b.scheduleSync(p.ID)
	}
	go b.maintenance()
	return nil
}

func (b *Bot) Stop() {
	close(b.stop)
	<-b.done
	// Closing the gateway stops new interactions; REST calls from pending
	// syncs still work, so let them finish.
	b.s.Close()
	b.syncWG.Wait()
}

func (b *Bot) registerCommands() error {
	commands := commandList(b.game)

	appID := b.s.State.User.ID
	registered, err := b.s.ApplicationCommandBulkOverwrite(appID, b.devGuild, commands)
	if err != nil {
		return fmt.Errorf("register slash commands: %w", err)
	}
	b.cmdMu.Lock()
	defer b.cmdMu.Unlock()
	for _, c := range registered {
		b.commandIDs[c.Name] = c.ID
	}
	return nil
}

// guard logs a panic in an event handler instead of crashing the bot. It
// must be deferred directly.
func guard(what string) {
	if r := recover(); r != nil {
		log.Printf("panic %s: %v\n%s", what, r, debug.Stack())
	}
}

func (b *Bot) onInteraction(_ *discordgo.Session, ic *discordgo.InteractionCreate) {
	defer guard("handling interaction")
	i := ic.Interaction
	// Whoever uses the bot on a server is a member of it.
	if i.GuildID != "" && i.Member != nil && i.Member.User != nil {
		b.members.set(i.GuildID, i.Member.User.ID, true, time.Now())
	}

	var err error
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "lfg":
			err = b.cmdLFG(i)
		case "lfg-find":
			err = b.cmdFind(i)
		case "lfg-notify":
			err = b.cmdNotify(i)
		case "lfg-stats":
			err = b.cmdStats(i)
		case "lfg-setup":
			err = b.cmdSetup(i)
		case "lfg-disable":
			err = b.cmdDisable(i)
		case "profile":
			err = b.cmdProfile(i)
		case "lfg-network":
			err = b.cmdNetwork(i)
		case "lfg-check":
			err = b.cmdCheck(i)
		case "help":
			err = b.cmdHelp(i, false)
		case "help-public":
			err = b.cmdHelp(i, true)
		case "legend":
			err = b.cmdLegend(i)
		}
	case discordgo.InteractionApplicationCommandAutocomplete:
		if i.ApplicationCommandData().Name == "lfg-network" {
			err = b.onNetworkAutocomplete(i)
		}
	case discordgo.InteractionModalSubmit:
		id := i.ModalSubmitData().CustomID
		if id == draftID(fieldDetails) {
			err = b.onDraftDetails(i)
		} else if id == idProfileModal {
			err = b.onProfileSubmit(i)
		} else if action, postID, ok := parseCustomID(id); ok && action == actJoinForm {
			err = b.onJoinSubmit(i, postID)
		} else if ok && action == actEditForm {
			err = b.onEditSubmit(i, postID)
		}
	case discordgo.InteractionMessageComponent:
		data := i.MessageComponentData()
		if field, ok := strings.CutPrefix(data.CustomID, draftPrefix); ok {
			err = b.onDraft(i, field, data.Values)
		} else if field, ok := strings.CutPrefix(data.CustomID, alertPrefix); ok {
			err = b.onAlert(i, field, data.Values)
		} else if data.CustomID == idFindJoin {
			err = b.onFindJoin(i, data.Values)
		} else if action, postID, ok := parseCustomID(data.CustomID); ok {
			err = b.onComponent(i, action, postID, data.Values)
		}
	}
	if err != nil {
		log.Printf("interaction %s: %v", describe(i), err)
	}
}

func describe(i *discordgo.Interaction) string {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		return "/" + i.ApplicationCommandData().Name
	case discordgo.InteractionModalSubmit:
		return i.ModalSubmitData().CustomID
	case discordgo.InteractionMessageComponent:
		return i.MessageComponentData().CustomID
	}
	return i.Type.String()
}

// onGuildDelete forgets a server's settings once the bot is removed from it.
// Outages also send this event, but with Unavailable set.
func (b *Bot) onGuildDelete(_ *discordgo.Session, g *discordgo.GuildDelete) {
	if g.Unavailable {
		return
	}
	if b.store.RemoveGuild(g.ID) {
		log.Printf("removed from guild %s, dropped its LFG settings", g.ID)
	}
	if n, err := b.store.LeaveNetwork(g.ID); err == nil {
		log.Printf("removed from guild %s, took it out of network %q", g.ID, n.Name)
	}
}

// maintenance expires old posts, reminds parties of their start and
// deletes ended posts.
func (b *Bot) maintenance() {
	defer close(b.done)
	tick := time.NewTicker(maintenanceEvery)
	defer tick.Stop()
	for {
		now := time.Now()
		for _, id := range b.store.ExpireDue(now) {
			b.scheduleSync(id)
		}
		for _, id := range b.store.DueReminders(b.game.Reminder(), now) {
			b.remind(id)
		}
		b.deleteEnded(now.Add(-keepEndedPosts))
		b.store.PruneRuns(now.Add(-keepRuns))
		b.dropStaleDrafts(now)
		b.members.dropStale(now)

		select {
		case <-b.stop:
			return
		case <-tick.C:
		}
	}
}

func (b *Bot) guildName(guildID string) string {
	if guildID == "" {
		return ""
	}
	if g, err := b.s.State.Guild(guildID); err == nil {
		return g.Name
	}
	return ""
}
