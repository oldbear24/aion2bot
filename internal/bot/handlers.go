package bot

import (
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

func (b *Bot) onComponent(i *discordgo.Interaction, action, postID string, values []string) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	// The dungeon change carries the leader's pick after the post's ID.
	postID, pick, _ := strings.Cut(postID, ":")
	p, ok := b.store.Post(postID)
	if !ok {
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, l)))
	}

	switch action {
	case actJoin:
		return b.onJoin(i, p, userID)
	case actLeave:
		return b.onLeave(i, p, userID)
	}

	// Everything else belongs to the leader, except what moderators may do
	// (see moderation.go).
	acc := b.access(i, p)
	need := accessLeader
	switch action {
	case actManage, actPanel, actHide, actBlock, actNetBlock:
		need = accessMod
	case actClose, actDelete, actDeleteYes, actHandTo, actHandYes:
		need = accessInCharge
	}
	if acc < need {
		return b.reply(i, notice(colorClosed, l.T("manage.only_leader")))
	}
	switch action {
	case actManage:
		return b.reply(i, b.panelFor(i, p))
	case actHide:
		return b.onHide(i, p)
	case actBlock, actNetBlock:
		return b.onBlock(i, p, values, action == actNetBlock)
	case actLooking:
		return b.onLookingFor(i, p, values)
	case actHandTo:
		if !p.Active() {
			return b.update(i, b.panelFor(i, p))
		}
		return b.update(i, b.handOverPanel(p, first(values), acc == accessLeader, l))
	case actHandYes:
		newLeader, how, _ := strings.Cut(pick, ":")
		return b.onHandOver(i, p, newLeader, how == "stay")
	case actAccept:
		return b.onAccept(i, p, values)
	case actKick:
		return b.onKick(i, p, values)
	case actMore, actLess:
		// A rally changes its size instead.
		if p.Rally {
			return b.update(i, b.managePanel(p, l))
		}
		delta := 1
		if action == actLess {
			delta = -1
		}
		limit := b.game.DungeonOrUnknown(p.DungeonID).MaxMissing(p.ModeID, b.game.MaxMissing)
		return b.leaderUpdate(i, p.ID, func(p *lfg.Post) error { return p.AdjustMissing(delta, limit) })
	case actSize:
		return b.onSize(i, p, first(values))
	case actClose:
		after, err := b.leaderChange(i, p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) })
		if acc != accessLeader && p.Active() && after.ID != "" && !after.Active() {
			b.tellLeader(after, i.GuildID, "dm.mod_closed")
		}
		return err
	case actEdit:
		if !p.Active() {
			return b.update(i, b.managePanel(p, l))
		}
		return b.showModal(i, b.editModal(p, b.timeZone(userID, i.Locale), l))
	case actPanel:
		return b.update(i, b.panelFor(i, p))
	case actReopen:
		return b.onReopen(i, p)
	case actMove, actMoveTo, actMoveMode:
		if !p.Active() {
			return b.update(i, b.managePanel(p, l))
		}
		dungeonID, modeID := p.DungeonID, p.ModeID
		switch action {
		case actMoveTo:
			if dungeonID = first(values); dungeonID != p.DungeonID {
				modeID = ""
			}
		case actMoveMode:
			dungeonID, modeID = pick, first(values)
		}
		return b.update(i, b.movePanel(p, dungeonID, modeID, l))
	case actMoveYes:
		dungeonID, modeID, _ := strings.Cut(pick, ":")
		return b.onMove(i, p, dungeonID, modeID)
	case actDelete:
		return b.update(i, b.confirmDelete(p, acc != accessLeader, l))
	case actDeleteYes:
		// Answer within Discord's 3 second limit, then fill in the result.
		if err := b.update(i, notice(colorClosed, l.T("delete.deleting"))); err != nil {
			log.Printf("acknowledge delete of %s: %v", p.ID, err)
		}
		deleted, failed := b.removePost(p.ID)
		msg := l.T("delete.done", l.N(deleted, "n.message"))
		if failed > 0 {
			msg += l.T("delete.failed", l.N(failed, "n.message"))
		}
		if acc != accessLeader {
			b.tellLeader(p, i.GuildID, "dm.mod_deleted")
		}
		return b.editReply(i, notice(colorClosed, msg))
	}
	return nil
}

// onLookingFor changes whom the post is looking for, from the manage
// panel. Players already on it stay; the threads hear about it.
func (b *Bot) onLookingFor(i *discordgo.Interaction, before lfg.Post, values []string) error {
	ids := b.normalizeLookingFor(values, before.LookingFor)
	p, err := b.leaderChange(i, before.ID, func(p *lfg.Post) error { return p.SetLookingFor(ids) })
	if p.ID != "" && !slices.Equal(before.LookingFor, p.LookingFor) {
		b.announceChanges(before, p)
	}
	return err
}

// onHandOver makes a party member the leader, as the leader or the
// moderators asked. The new leader is pinged in the thread, on their own
// server; reserves who move up into a slot the old leader freed are
// welcomed as usual.
func (b *Bot) onHandOver(i *discordgo.Interaction, before lfg.Post, userID string, stay bool) error {
	l := lang(i)
	actor, _ := interactionUser(i)
	byLeader := actor == before.LeaderID
	var promoted []lfg.Player
	p, err := b.store.HandOverPost(before.ID, userID, b.game.MaxRunsPerLeader, func(p *lfg.Post) error {
		r, err := p.HandOver(userID, stay)
		if err != nil {
			return err
		}
		promoted = firstPings(p, r)
		return nil
	})
	switch {
	case errors.Is(err, lfg.ErrTooManyPosts):
		name := userID
		if pl, ok := personOn(before, userID); ok {
			name = pl.Name
		}
		return b.update(i, notice(colorClosed, l.T("hand.too_many", escape(name), b.game.MaxRunsPerLeader)))
	case errors.Is(err, lfg.ErrNotFound):
		return b.update(i, notice(colorClosed, errorText(err, l)))
	case err != nil:
		// The post ended or the player left meanwhile; show it as it is.
		return b.update(i, b.panelFor(i, p))
	}
	b.scheduleSync(p.ID)
	err = b.update(i, notice(colorOpen, l.T("hand.done", b.game.LeaderEmoji, escape(p.LeaderName), b.runName(p))))

	go b.renameThreads(p)
	b.learnMembers(p, p.GuildIDs())
	byGuild := ""
	if !byLeader {
		byGuild = i.GuildID
	}
	note := func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.handOverNote(before, p, stay, byGuild, guildID, l)
	}
	if !b.announce(p, map[string][]string{p.GuildID: {p.LeaderID}}, true, note)[p.GuildID] {
		ll := b.guildLang(p.GuildID)
		b.dm(p.LeaderID, container(colorOpen, text(ll.T("dm.new_leader", b.game.LeaderEmoji, b.runName(p))), manageRow(p.ID, ll)))
	}
	if !byLeader {
		b.tellLeader(before, i.GuildID, "dm.mod_handed")
	}
	b.welcome(p, promoted, true)
	return err
}

// onJoin opens the join form after checking the player can join at all.
func (b *Bot) onJoin(i *discordgo.Interaction, p lfg.Post, userID string) error {
	var err error
	switch {
	case userID == p.LeaderID:
		err = lfg.ErrIsLeader
	case p.Has(userID):
		err = lfg.ErrAlreadyJoined
	case !p.Active():
		err = lfg.ErrNotOpen
	case p.Crowded():
		err = lfg.ErrTooManyJoined
	}
	if err != nil {
		return b.reply(i, notice(colorClosed, errorText(err, lang(i))))
	}
	if b.blockedFrom(p, userID, i.GuildID) {
		return b.reply(i, notice(colorClosed, lang(i).T("blocked.join")))
	}
	prof, _ := b.store.Profile(userID)
	return b.showModal(i, b.joinModal(p, prof, lang(i)))
}

// blockedFrom reports whether a player may not join a post: they are
// blocked on the server they join from, or on the post's own server.
func (b *Bot) blockedFrom(p lfg.Post, userID, guildID string) bool {
	return b.store.Blocked(userID, guildID) || b.store.Blocked(userID, p.GuildID)
}

// onJoinSubmit adds the player once they sent their role, class, gear
// score and character name: on the list for the leader, or as a reserve
// when the party is full.
func (b *Bot) onJoinSubmit(i *discordgo.Interaction, postID string) error {
	userID, name := interactionUser(i)
	l := lang(i)
	values := modalValues(i.ModalSubmitData().Components)
	classID, roleID, _ := strings.Cut(first(values["choice"]), "|")
	gs, gsErr := parseGS(first(values["gs"]))

	// The form asks for the faction and game server only when the profile
	// doesn't fit; otherwise the profile's count.
	prof, _ := b.store.Profile(userID)
	faction, server := prof.Faction, prof.Server
	if v, ok := values["faction"]; ok {
		faction = b.knownFaction(first(v))
	}
	if v, ok := values["server"]; ok {
		server = cleanChar(first(v))
	}

	p, ok := b.store.Post(postID)
	switch {
	case !ok:
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, l)))
	case b.blockedFrom(p, userID, i.GuildID):
		return b.reply(i, notice(colorClosed, l.T("blocked.join")))
	case !b.game.Fits(p.LookingFor, classID, roleID):
		return b.reply(i, notice(colorClosed, l.T("join.class_gone")))
	case gsErr != nil:
		return b.reply(i, notice(colorClosed, l.T("join.bad_gs")))
	case p.Faction != "" && faction != "" && faction != p.Faction:
		return b.reply(i, notice(colorClosed, l.T("join.wrong_faction", b.factionTag(p.Faction), b.factionTag(faction))))
	case !sideFits(p, faction, server):
		return b.reply(i, notice(colorClosed, l.T("join.wrong_server", escape(p.Server), escape(server))))
	}

	pl := lfg.Player{UserID: userID, Name: name, ClassID: classID, RoleID: roleID, GS: gs,
		GuildID: i.GuildID, GuildName: b.guildName(i.GuildID), Char: cleanChar(first(values["char"]))}
	var pingLeader bool
	p, err := b.store.UpdatePost(postID, func(p *lfg.Post) error {
		if err := p.AddApplicant(pl); err != nil {
			return err
		}
		pingLeader = p.MarkPinged(p.LeaderID)
		return nil
	})
	if errors.Is(err, lfg.ErrGearScore) {
		return b.reply(i, notice(colorClosed, l.T("join.gs_low", l.GS(p.MinGS), l.GS(gs))))
	}
	if err != nil {
		return b.reply(i, notice(colorClosed, errorText(err, l)))
	}
	b.scheduleSync(p.ID)
	b.rememberJoin(pl, faction, server)

	// A rally takes the player straight in, so they hear whom to look for
	// in game right away.
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	reserve := p.IsReserve(userID)
	if reserve {
		err = b.reply(i, notice(colorFull, l.T(textFor("join.reserve", p.Rally), dungeon.Name, b.bringing(p, pl), len(p.Reserves))))
	} else if p.Rally {
		err = b.reply(i, notice(colorOpen, l.T("join.abyss_done", dungeon.Name, b.bringing(p, pl), escape(p.LeaderName))+
			leaderCharLine(p, l)))
	} else {
		err = b.reply(i, notice(colorOpen, l.T("join.done", dungeon.Name, b.bringing(p, pl), escape(p.LeaderName))))
	}

	// The leader is pinged for the first player only, on their own server;
	// the note shows in the thread on every server, and the card shows the
	// rest. Everyone who joins is added to the threads.
	b.learnMembers(p, p.GuildIDs())
	if pingLeader {
		note := func(guildID string, l i18n.Lang) discordgo.MessageComponent {
			return b.joinNote(p, pl, reserve, guildID, l)
		}
		if !b.announce(p, map[string][]string{p.GuildID: {p.LeaderID}}, true, note)[p.GuildID] {
			ll := b.guildLang(p.GuildID)
			b.dm(p.LeaderID, container(colorOpen,
				text(ll.T(textFor("dm.leader", p.Rally), dungeon.Name, b.playerLine(p, pl, ll))), manageRow(p.ID, ll)))
		}
	}
	if p, ok := b.store.Post(p.ID); ok {
		b.addToThreads(p.Messages, []lfg.Player{pl})
	}
	return err
}

func (b *Bot) onLeave(i *discordgo.Interaction, p lfg.Post, userID string) error {
	l := lang(i)
	if userID == p.LeaderID {
		return b.reply(i, notice(colorClosed, l.T("leave.leader")))
	}
	// Leaving only updates the card; the leader was already pinged once. A
	// reserve who takes the slot is welcomed like an accepted player.
	p, promoted, err := b.dropPlayer(p.ID, userID)
	if err != nil {
		return b.reply(i, notice(colorClosed, errorText(err, l)))
	}
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	err = b.reply(i, notice(colorClosed, l.T("leave.done", dungeon.Name)))
	b.welcome(p, promoted, true)
	return err
}

// dropPlayer takes a player off an open post. It returns the reserves who
// moved up into the slot and weren't welcomed before; the caller welcomes
// them once it has answered the interaction.
func (b *Bot) dropPlayer(postID, userID string) (lfg.Post, []lfg.Player, error) {
	var promoted []lfg.Player
	p, err := b.store.UpdatePost(postID, func(p *lfg.Post) error {
		if !p.Active() {
			return lfg.ErrNotOpen
		}
		r, ok := p.Remove(userID)
		if !ok {
			return lfg.ErrNotInParty
		}
		promoted = firstPings(p, r)
		return nil
	})
	if err == nil {
		b.scheduleSync(p.ID)
	}
	return p, promoted, err
}

// firstPings marks the reserves that moved up into the party as pinged
// and returns those who weren't before. A reserve removed in the same go
// is left out.
func firstPings(p *lfg.Post, removals ...lfg.Removal) []lfg.Player {
	var fresh []lfg.Player
	for _, r := range removals {
		if r.Promoted != nil && p.InParty(r.Promoted.UserID) && p.MarkPinged(r.Promoted.UserID) {
			fresh = append(fresh, *r.Promoted)
		}
	}
	return fresh
}

func (b *Bot) onAccept(i *discordgo.Interaction, p lfg.Post, userIDs []string) error {
	// Players are pinged the first time they get into this party only.
	var fresh []lfg.Player
	p, err := b.leaderChange(i, p.ID, func(p *lfg.Post) error {
		accepted, err := p.Accept(userIDs)
		if err != nil {
			return err
		}
		fresh = fresh[:0]
		for _, pl := range accepted {
			if p.MarkPinged(pl.UserID) {
				fresh = append(fresh, pl)
			}
		}
		return nil
	})
	b.welcome(p, fresh, false)
	return err
}

// welcome pings players who just got into the party, each on the server
// they joined from; the note shows in the thread on every server, in its
// language. promoted says they moved up from the reserves.
func (b *Bot) welcome(p lfg.Post, players []lfg.Player, promoted bool) {
	if len(players) == 0 {
		return
	}
	dmKey := "dm.accepted"
	if promoted {
		dmKey = "dm.promoted"
	}
	where := b.runName(p)
	b.learnMembers(p, p.GuildIDs())
	for _, pl := range players {
		note := func(guildID string, l i18n.Lang) discordgo.MessageComponent {
			return b.welcomeNote(p, pl, promoted, guildID, l)
		}
		if !b.announce(p, map[string][]string{pl.GuildID: {pl.UserID}}, true, note)[pl.GuildID] {
			pll := b.guildLang(pl.GuildID)
			b.dm(pl.UserID, notice(colorFull, pll.T(dmKey, escape(p.LeaderName), p.LeaderID, where, b.bringing(p, pl),
				postStart(p, pll))+leaderCharLine(p, pll)))
		}
	}
	// They joined the threads as applicants; this covers anyone who wasn't
	// added then.
	if p, ok := b.store.Post(p.ID); ok {
		b.addToThreads(p.Messages, players)
	}
}

// runName is "**Krao Cave** (Conquest)".
func (b *Bot) runName(p lfg.Post) string {
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	s := "**" + dungeon.Name + "**"
	if m, ok := dungeon.Mode(p.ModeID); ok {
		s += " (" + m.Name + ")"
	}
	return s
}

// leaderCharLine tells players who got a DM whom to ask for the invite
// in game, when the leader saved their character name.
func leaderCharLine(p lfg.Post, l i18n.Lang) string {
	if p.LeaderChar == "" {
		return ""
	}
	return "\n" + l.T("ping.leader_char", charTag(p.LeaderChar))
}

// onEditSubmit applies a new gear score, start and note from the form on
// the manage panel, and redraws the panel. The threads hear what changed.
func (b *Bot) onEditSubmit(i *discordgo.Interaction, postID string) error {
	userID, _ := interactionUser(i)
	l := lang(i)
	before, ok := b.store.Post(postID)
	switch {
	case !ok:
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, l)))
	case userID != before.LeaderID:
		return b.reply(i, notice(colorClosed, l.T("manage.only_leader")))
	}
	values := modalValues(i.ModalSubmitData().Components)
	gs, err := parseGS(first(values["gs"]))
	if err != nil {
		return b.reply(i, notice(colorClosed, l.T("draft.bad_gs")))
	}
	// The form shows the current start; left as it was, it stays even once
	// it has passed.
	now := time.Now()
	zone := b.timeZone(userID, i.Locale)
	startAt := before.StartAt
	if input := strings.TrimSpace(first(values["start"])); input != startInput(before.StartAt, zone) {
		at, err := parseStart(input, zone, now)
		switch {
		case err != nil:
			return b.reply(i, notice(colorClosed, l.T("draft.bad_start")))
		case !at.IsZero() && !at.After(now):
			return b.reply(i, notice(colorClosed, l.T("draft.start_past")))
		}
		startAt = at
	}
	note := strings.TrimSpace(first(values["note"]))

	server, askedServer := values["server"]
	p, err := b.store.UpdatePost(postID, func(p *lfg.Post) error {
		if askedServer && p.Rally {
			if err := p.SetServer(cleanChar(first(server))); err != nil {
				return err
			}
		}
		return p.SetDetails(gs, startAt, note, b.game.PostTTL(), now)
	})
	if err != nil {
		return b.reply(i, notice(colorClosed, errorText(err, l)))
	}
	b.scheduleSync(p.ID)
	err = b.update(i, b.managePanel(p, l))
	b.announceChanges(before, p)
	return err
}

// announceChanges tells the existing threads what the leader changed,
// without pinging anyone.
func (b *Bot) announceChanges(before, after lfg.Post) {
	if before.StartAt.Equal(after.StartAt) && before.MinGS == after.MinGS && before.Note == after.Note &&
		slices.Equal(before.LookingFor, after.LookingFor) && before.Server == after.Server {
		return
	}
	b.learnMembers(after, after.GuildIDs())
	b.announce(after, nil, false, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.changesNote(before, after, guildID, l)
	})
}

// onReopen puts an ended post back up with everyone who was on it, if the
// leader has room for another open run. The threads hear about it, without
// pinging anyone.
func (b *Bot) onReopen(i *discordgo.Interaction, p lfg.Post) error {
	l := lang(i)
	p, err := b.store.ReopenPost(p.ID, b.game.MaxRunsPerLeader, b.game.PostTTL(), time.Now())
	switch {
	case errors.Is(err, lfg.ErrTooManyPosts):
		return b.reply(i, b.tooManyRuns(b.store.ActivePostsByLeader(p.LeaderID), l))
	case errors.Is(err, lfg.ErrNotFound):
		return b.update(i, notice(colorClosed, errorText(err, l)))
	case err != nil:
		// It was reopened meanwhile; show it as it is.
		return b.update(i, b.managePanel(p, l))
	}
	b.scheduleSync(p.ID)
	err = b.update(i, b.managePanel(p, l))
	b.learnMembers(p, p.GuildIDs())
	b.announce(p, nil, false, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.reopenNote(p, guildID, l)
	})
	return err
}

// onMove takes the post, with everyone on it, to another dungeon or mode.
// The cards and the thread names follow, and the threads hear about it
// without pinging anyone.
func (b *Bot) onMove(i *discordgo.Interaction, before lfg.Post, dungeonID, modeID string) error {
	// A dungeon with modes needs one of them; one without takes none. A
	// rally stays among the Abyss events, and a dungeon post among the
	// dungeons.
	to, ok := b.game.Dungeon(dungeonID)
	if _, hasMode := to.Mode(modeID); !ok || to.Abyss != before.Rally || !hasMode && (len(to.Modes) > 0 || modeID != "") {
		return b.update(i, b.managePanel(before, lang(i)))
	}
	p, err := b.leaderChange(i, before.ID, func(p *lfg.Post) error {
		fromSize := b.game.DungeonOrUnknown(p.DungeonID).PartySizeFor(p.ModeID)
		return p.Move(to.ID, modeID, fromSize, to.PartySizeFor(modeID), to.MaxMissing(modeID, b.game.MaxMissing))
	})
	// Nothing moved: the party didn't fit, or the post is gone.
	if p.ID == "" || p.DungeonID == before.DungeonID && p.ModeID == before.ModeID {
		return err
	}
	go b.renameThreads(p)
	b.learnMembers(p, p.GuildIDs())
	b.announce(p, nil, false, func(guildID string, l i18n.Lang) discordgo.MessageComponent {
		return b.moveNote(before, p, guildID, l)
	})
	return err
}

// onSize gives a rally a new group size from the manage panel. Reserves
// move up into the room it makes and are welcomed like a player who moved
// up when someone left.
func (b *Bot) onSize(i *discordgo.Interaction, p lfg.Post, value string) error {
	size, err := strconv.Atoi(value)
	if err != nil || !p.Rally || !slices.Contains(rallySizes, size) {
		return b.update(i, b.managePanel(p, lang(i)))
	}
	var fresh []lfg.Player
	p, err = b.leaderChange(i, p.ID, func(p *lfg.Post) error {
		moved, err := p.SetSize(size)
		if err != nil {
			return err
		}
		fresh = fresh[:0]
		for _, pl := range moved {
			if p.MarkPinged(pl.UserID) {
				fresh = append(fresh, pl)
			}
		}
		return nil
	})
	b.welcome(p, fresh, true)
	return err
}

func (b *Bot) onKick(i *discordgo.Interaction, p lfg.Post, userIDs []string) error {
	var removed []lfg.Removal
	var promoted []lfg.Player
	p, err := b.leaderChange(i, p.ID, func(p *lfg.Post) error {
		if !p.Active() {
			return lfg.ErrNotOpen
		}
		removed = removed[:0]
		for _, id := range userIDs {
			if r, ok := p.Remove(id); ok {
				removed = append(removed, r)
			}
		}
		if len(removed) == 0 {
			return lfg.ErrNotInParty
		}
		promoted = firstPings(p, removed...)
		return nil
	})
	dungeon := b.game.DungeonOrUnknown(p.DungeonID)
	for _, r := range removed {
		pll := b.guildLang(r.Player.GuildID)
		msg := pll.T("dm.declined", escape(p.LeaderName), dungeon.Name)
		if r.WasAccepted {
			msg = pll.T("dm.removed", escape(p.LeaderName), dungeon.Name)
		}
		b.dm(r.Player.UserID, notice(colorClosed, msg))
	}
	b.welcome(p, promoted, true)
	return err
}

func (b *Bot) leaderUpdate(i *discordgo.Interaction, postID string, fn func(p *lfg.Post) error) error {
	_, err := b.leaderChange(i, postID, fn)
	return err
}

// leaderChange applies a change from the manage panel, or a moderator's,
// and redraws it. If the change is rejected (for example the post ended
// meanwhile), the panel still redraws so it shows the current state.
func (b *Bot) leaderChange(i *discordgo.Interaction, postID string, fn func(p *lfg.Post) error) (lfg.Post, error) {
	p, err := b.store.UpdatePost(postID, fn)
	switch {
	case errors.Is(err, lfg.ErrNotFound):
		return p, b.update(i, notice(colorClosed, errorText(err, lang(i))))
	case err == nil:
		b.scheduleSync(postID)
	}
	return p, b.update(i, b.panelFor(i, p))
}

// /lfg-setup picks this server's LFG channel, ping role and language, and
// sends the channel the posts that are already open.
func (b *Bot) cmdSetup(i *discordgo.Interaction) error {
	l := lang(i)
	data := i.ApplicationCommandData()
	var channelID, roleID, langCode string
	for _, o := range data.Options {
		switch o.Name {
		case "channel":
			channelID = o.ChannelValue(nil).ID
		case "ping_role":
			roleID = o.RoleValue(nil, "").ID
		case "language":
			langCode = o.StringValue()
		}
	}
	if roleID == i.GuildID {
		return b.reply(i, notice(colorClosed, l.T("setup.everyone")))
	}

	perms, err := b.s.UserChannelPermissions(b.s.State.User.ID, channelID)
	if err != nil {
		log.Printf("check permissions in %s: %v", channelID, err)
		return b.reply(i, notice(colorClosed, l.T("setup.cant_check", channelID)))
	}
	report, ready := setupReport(perms, channelID, data.Resolved.Roles[roleID], l)
	if !ready {
		return b.reply(i, notice(colorClosed, l.T("setup.not_ready", channelID, report)))
	}

	cfg := lfg.GuildConfig{GuildID: i.GuildID, GuildName: b.guildName(i.GuildID), ChannelID: channelID, PingRoleID: roleID,
		Lang: string(b.setupLang(i, langCode))}
	if err := b.store.SetGuild(cfg); err != nil {
		b.reply(i, notice(colorClosed, errorText(err, l)))
		return err
	}

	msg := l.T("setup.done", channelID, report) + l.T("setup.language", i18n.Parse(cfg.Lang).Name())
	if _, linked := b.store.NetworkOf(i.GuildID); !linked {
		msg += l.T("setup.not_linked")
	}
	active := b.visiblePosts(i.GuildID)
	if len(active) > 0 {
		msg += l.T("setup.sending", l.N(len(active), "n.post"))
	}
	err = b.reply(i, notice(colorOpen, msg))

	for _, p := range active {
		b.broadcast(p, []lfg.GuildConfig{cfg}, false)
	}
	return err
}

// setupLang decides a server's post language: the one picked in the
// command, else the one set before, else the server's own locale if the
// bot speaks it, else the admin's language.
func (b *Bot) setupLang(i *discordgo.Interaction, picked string) i18n.Lang {
	if picked != "" {
		return i18n.Parse(picked)
	}
	if prev, ok := b.store.Guild(i.GuildID); ok {
		return i18n.Parse(prev.Lang)
	}
	if i.GuildLocale != nil && i18n.Parse(string(*i.GuildLocale)) != i18n.English {
		return i18n.Parse(string(*i.GuildLocale))
	}
	return lang(i)
}

// visiblePosts are the open posts made on this server or a linked one,
// except those its moderators took off it and those of players they
// blocked.
func (b *Bot) visiblePosts(guildID string) []lfg.Post {
	linked := b.store.Linked(guildID)
	var out []lfg.Post
	for _, p := range b.store.ActivePosts() {
		if slices.Contains(linked, p.GuildID) && b.welcomeIn(p, guildID) {
			out = append(out, p)
		}
	}
	return out
}

func (b *Bot) cmdDisable(i *discordgo.Interaction) error {
	l := lang(i)
	if !b.store.RemoveGuild(i.GuildID) {
		return b.reply(i, notice(colorClosed, l.T("disable.none")))
	}
	return b.reply(i, notice(colorClosed, l.T("disable.done")))
}

func manageRow(postID string, l i18n.Lang) discordgo.ActionsRow {
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{manageButton(postID, false, l)}}
}

// playerLine is a player as the leader sees them: name, mention, role,
// class and home server.
func (b *Bot) playerLine(p lfg.Post, pl lfg.Player, l i18n.Lang) string {
	s := l.T("player.as", escape(pl.Name)+charSuffix(pl.Char), pl.UserID, b.bringing(p, pl))
	if pl.GS > 0 {
		s += " (GS " + l.GS(pl.GS) + ")"
	}
	if pl.GuildName != "" {
		s += l.T("player.from", escape(pl.GuildName))
	}
	return s
}

// normalizeLookingFor keeps known roles, then known classes, in config
// order. "Any class" can't be combined with specific picks, so the newest
// choice wins: ticking a role or class while "Any class" was selected
// (previous) drops it, and ticking "Any class" clears the rest.
func (b *Bot) normalizeLookingFor(ids, previous []string) []string {
	if slices.Contains(ids, config.AnyID) {
		if !slices.Contains(previous, config.AnyID) || len(ids) == 1 {
			return []string{config.AnyID}
		}
		ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == config.AnyID })
	}
	var out []string
	for _, r := range b.game.Roles {
		if slices.Contains(ids, r.ID) {
			out = append(out, r.ID)
		}
	}
	for _, c := range b.game.Classes {
		if slices.Contains(ids, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}

func errorText(err error, l i18n.Lang) string {
	switch {
	case errors.Is(err, lfg.ErrNotFound):
		return l.T("err.not_found")
	case errors.Is(err, lfg.ErrNotOpen):
		return l.T("err.not_open")
	case errors.Is(err, lfg.ErrIsLeader):
		return l.T("err.is_leader")
	case errors.Is(err, lfg.ErrAlreadyJoined):
		return l.T("err.already_joined")
	case errors.Is(err, lfg.ErrNotInParty):
		return l.T("err.not_in_party")
	case errors.Is(err, lfg.ErrTooManyJoined):
		return l.T("err.too_many_joined")
	case errors.Is(err, lfg.ErrTooManyPosts):
		return l.T("err.too_many_posts")
	}
	return l.T("err.generic")
}

// modalValues maps each input's custom ID to its submitted value(s).
func modalValues(comps []discordgo.MessageComponent) map[string][]string {
	out := map[string][]string{}
	var walk func(c discordgo.MessageComponent)
	walk = func(c discordgo.MessageComponent) {
		switch v := c.(type) {
		case *discordgo.Label:
			walk(v.Component)
		case *discordgo.ActionsRow:
			for _, cc := range v.Components {
				walk(cc)
			}
		case *discordgo.SelectMenu:
			out[v.CustomID] = v.Values
		case *discordgo.TextInput:
			out[v.CustomID] = []string{v.Value}
		}
	}
	for _, c := range comps {
		walk(c)
	}
	return out
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// setupChecks are the permissions /lfg-setup checks the bot has in the LFG
// channel. Without the required ones nothing can be posted at all; without
// the others pings go by DM. key names the permission and the reason for
// it in the catalog.
//
// Nothing else is needed: the bot only edits and deletes its own messages
// and threads, reads only what players write in the threads under posts,
// and answers commands, buttons and forms through interactions, which
// need no permission.
var setupChecks = []struct {
	perm     int64
	key      string
	required bool
}{
	{discordgo.PermissionViewChannel, "perm.view", true},
	{discordgo.PermissionSendMessages, "perm.send", true},
	{discordgo.PermissionCreatePublicThreads | discordgo.PermissionSendMessagesInThreads, "perm.threads", false},
	{discordgo.PermissionManageWebhooks, "perm.webhooks", false},
}

// setupReport is the ✅/⚠️/❌ checklist of the bot's permissions in the
// channel and of the ping role. ready is false when a required permission
// is missing.
func setupReport(perms int64, channelID string, pingRole *discordgo.Role, l i18n.Lang) (report string, ready bool) {
	ready = true
	var sb strings.Builder
	sb.WriteString(l.T("report.title", channelID))
	for _, c := range setupChecks {
		switch {
		case perms&c.perm == c.perm:
			sb.WriteString("\n✅ " + l.T(c.key))
		case c.required:
			ready = false
			sb.WriteString(l.T("report.missing", l.T(c.key), l.T(c.key+".why")))
		default:
			sb.WriteString(l.T("report.warn", l.T(c.key), l.T(c.key+".why")))
		}
	}

	if pingRole != nil {
		sb.WriteString(l.T("report.ping", pingRole.ID))
		switch {
		case pingRole.Managed:
			sb.WriteString(l.T("report.ping_bot"))
		case pingRole.Mentionable || perms&discordgo.PermissionMentionEveryone != 0:
			sb.WriteString(l.T("report.ping_ok"))
		default:
			sb.WriteString(l.T("report.ping_quiet"))
		}
	}
	return sb.String(), ready
}

// /lfg-check runs the setup checklist again, on the LFG channel or on any
// channel, and shows how the server is linked.
func (b *Bot) cmdCheck(i *discordgo.Interaction) error {
	l := lang(i)
	cfg, configured := b.store.Guild(i.GuildID)
	channelID := cfg.ChannelID
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "channel" {
			channelID = o.ChannelValue(nil).ID
		}
	}
	if channelID == "" {
		return b.reply(i, notice(colorClosed, l.T("check.no_channel")))
	}

	perms, err := b.s.UserChannelPermissions(b.s.State.User.ID, channelID)
	if err != nil {
		log.Printf("check permissions in %s: %v", channelID, err)
		return b.reply(i, notice(colorClosed, l.T("check.cant_see", channelID)))
	}

	isLFG := configured && channelID == cfg.ChannelID
	var pingRole *discordgo.Role
	roleGone := false
	if isLFG && cfg.PingRoleID != "" {
		if pingRole, err = b.s.State.Role(i.GuildID, cfg.PingRoleID); err != nil {
			pingRole, roleGone = nil, true
		}
	}
	report, ready := setupReport(perms, channelID, pingRole, l)

	head := l.T("check.ok")
	if !ready {
		head = l.T("check.blocked")
	} else if strings.Contains(report, "⚠️") || roleGone {
		head = l.T("check.attention")
	}
	comps := []discordgo.MessageComponent{text(head), text(report)}

	var notes []string
	switch {
	case isLFG:
		notes = append(notes, l.T("check.is_lfg", channelID))
	case configured:
		notes = append(notes, l.T("check.other", cfg.ChannelID))
	default:
		notes = append(notes, l.T("check.unset"))
	}
	if roleGone {
		notes = append(notes, l.T("check.role_gone"))
	}
	if configured {
		notes = append(notes, l.T("check.language", i18n.Parse(cfg.Lang).Name()))
	}
	if n, ok := b.store.NetworkOf(i.GuildID); ok {
		notes = append(notes, l.T("check.linked", escape(n.Name), l.N(len(n.Members)-1, "n.other_server")))
	} else {
		notes = append(notes, l.T("check.unlinked"))
	}
	comps = append(comps, divider(), text(strings.Join(notes, "\n")))

	color := colorOpen
	if !ready {
		color = colorClosed
	}
	return b.reply(i, container(color, comps...))
}
