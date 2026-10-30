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

	"aion2bot/internal/config"
	"aion2bot/internal/lfg"
)

const (
	maintenanceEvery = time.Minute
	// Ended (closed or expired) posts are deleted from every channel, along
	// with the bot's replies under them, this long after they end.
	keepEndedPosts = 24 * time.Hour
	// Posts deleted per maintenance round, to stay clear of rate limits.
	deleteBatch = 20
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
	s.Identify.Intents = discordgo.IntentsGuilds

	b := &Bot{
		s:        s,
		game:     game,
		store:    store,
		devGuild: devGuild,
		syncs:    map[string]*syncState{},
		drafts:   map[string]*draft{},

		commandIDs: map[string]string{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	s.AddHandler(b.onInteraction)
	s.AddHandler(b.onGuildDelete)
	return b, nil
}

func (b *Bot) Start() error {
	if err := b.s.Open(); err != nil {
		return fmt.Errorf("connect to Discord: %w", err)
	}
	if err := b.registerCommands(); err != nil {
		b.s.Close()
		return err
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
	commands := commandList()

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

func (b *Bot) onInteraction(_ *discordgo.Session, ic *discordgo.InteractionCreate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic handling interaction: %v\n%s", r, debug.Stack())
		}
	}()
	i := ic.Interaction

	var err error
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "lfg":
			err = b.cmdLFG(i)
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
		}
	case discordgo.InteractionMessageComponent:
		data := i.MessageComponentData()
		if field, ok := strings.CutPrefix(data.CustomID, draftPrefix); ok {
			err = b.onDraft(i, field, data.Values)
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

// maintenance expires old posts and deletes ended ones.
func (b *Bot) maintenance() {
	defer close(b.done)
	tick := time.NewTicker(maintenanceEvery)
	defer tick.Stop()
	for {
		now := time.Now()
		for _, id := range b.store.ExpireDue(now) {
			b.scheduleSync(id)
		}
		b.deleteEnded(now.Add(-keepEndedPosts))
		b.dropStaleDrafts(now)

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
