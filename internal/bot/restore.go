package bot

import (
	"errors"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// A post the bot lost, along with its record of the card, can be put back from
// the card: Discord sends a message along with a click on it, so reading it
// needs no permission. It shows nearly all there is to a post.
//
// The card links people with mentions only where Discord can resolve them.
// The others are looked for by name on the server the card says they're from.
// Whoever can't be found has to join again, and their place in the party opens
// up. What isn't on a card (who moderators took it off for, whom a full post
// was looking for) starts over.

// memberSearchLimit is how many members a search by name looks through.
// Discord matches the start of a name, so the one sought may not come first.
const memberSearchLimit = 100

var (
	cardStart    = regexp.MustCompile(`<t:(\d+)(?::\w)?>`)
	cardFill     = regexp.MustCompile(`\((\d+)/(\d+)\)`)
	cardCount    = regexp.MustCompile(`\((\d+)\)\*\*$`)
	cardNumber   = regexp.MustCompile(`\d+`)
	anyCustom    = regexp.MustCompile(`<a?:\w+:\d+>`)
	iconHashPart = regexp.MustCompile(`_[0-9a-f]{6}$`)
	// What memberLine writes: the colour of their role and the leader's crown,
	// a mention or a name, then the character, class, gear score and home
	// server, each if there is one.
	cardMember = regexp.MustCompile("^(?:(\\S+) )?(?:(\\S+) )?(?:<@(\\d+)>|\\*\\*(.+?)\\*\\*)" +
		"(?: · `([^`]*)`)?(?: — (.+?))?(?: · GS ([\\d,.\\x{a0} ]+))?(?: · \\*(.+)\\*)?$")
)

// cardRead is what a card says about its post. Its people have an ID where
// the card links them, and otherwise only a name.
type cardRead struct {
	post   lfg.Post
	leader lfg.Player
	// shown is how many the card counts in the party, the leader included.
	// filled and size are the party's "(9/10)", if it shows them; missing is
	// the number it is looking for, for a dungeon with no known size.
	shown, filled, size, missing int
}

// onRestore puts a lost post back from its card, for its leader or a moderator
// in charge of it, under its old ID. The card takes up its place again, and the
// post carries on as if nothing happened, except for the people who can't be
// found.
func (b *Bot) onRestore(i *discordgo.Interaction, postID string) error {
	l := lang(i)
	userID, _ := interactionUser(i)
	if i.Message == nil {
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, l)))
	}
	r, ok := b.readCard(i.Message.Components, postID)
	switch {
	case !ok:
		log.Printf("can't read the card %s of lost post %s", i.Message.ID, postID)
		return b.reply(i, notice(colorClosed, l.T("restore.unreadable", b.cmd("lfg"))))
	case r.leader.UserID != "" && r.leader.UserID != userID && !moderates(i):
		return b.reply(i, notice(colorClosed, l.T("restore.not_allowed")))
	}
	// Answer within Discord's 3 second limit, then fill in the result.
	if err := b.reply(i, notice(colorFull, l.T("restore.working"))); err != nil {
		log.Printf("acknowledge reopening %s: %v", postID, err)
	}

	b.findPeople(&r, i.GuildID)
	if r.leader.UserID == "" {
		return b.editReply(i, notice(colorClosed, l.T("restore.no_leader")))
	}
	p, left := b.restoredPost(r, i.Message.ID, i.GuildID, time.Now())
	switch {
	case b.access(i, p) < accessInCharge:
		return b.editReply(i, notice(colorClosed, l.T("restore.not_allowed")))
	case b.store.Blocked(p.LeaderID, i.GuildID):
		return b.editReply(i, notice(colorClosed, l.T("restore.blocked")))
	}
	stored, ended, err := b.store.RestorePost(p, cardRef(i), b.game.MaxRunsPerLeader)
	switch {
	case errors.Is(err, lfg.ErrStillOpen):
		return b.editReply(i, notice(colorOpen, l.T("restore.already")))
	case errors.Is(err, lfg.ErrTooManyPosts):
		return b.editReply(i, b.tooManyRuns(b.store.ActivePostsByLeader(p.LeaderID), l))
	case err != nil:
		log.Printf("reopen lost post %s: %v", postID, err)
		return b.editReply(i, notice(colorClosed, errorText(err, l)))
	}
	log.Printf("reopened lost post %s from card %s in guild %s, %d people on it, %d left out",
		postID, i.Message.ID, i.GuildID, len(stored.People()), len(left))
	if i.Message.Thread != nil {
		b.store.SetThread(postID, i.Message.ID)
	}
	b.scheduleSync(postID)
	err = b.editReply(i, notice(colorOpen, l.T("restore.done", postID, l.N(len(stored.People()), "n.player"))+rejoinLine(left, l)))

	// A post that ended had its copies on the other servers deleted, so they
	// get new ones. After a lost database, they are still up, and follow when
	// someone clicks them (see adoptCard).
	if ended {
		b.broadcast(stored, b.store.Targets(stored.GuildID), false)
	}
	stored, ok = b.store.Post(postID)
	if !ok {
		return err
	}
	b.learnMembers(stored, stored.GuildIDs())
	if ref, ok := stored.MessageIn(i.GuildID); ok {
		b.startThreads(stored, []lfg.MessageRef{ref})
	}
	if stored, ok = b.store.Post(postID); ok {
		b.announce(stored, nil, false, func(_ string, l i18n.Lang) discordgo.MessageComponent {
			return b.restoredNote(stored, left, l)
		})
	}
	return err
}

// rejoinLine names the players who have to join again, if any.
func rejoinLine(left []string, l i18n.Lang) string {
	if len(left) == 0 {
		return ""
	}
	names := make([]string, 0, len(left))
	for _, name := range left {
		names = append(names, "**"+escape(name)+"**")
	}
	return l.T("restore.rejoin", strings.Join(names, ", "))
}

// adoptCard takes back a card the bot had forgotten, when someone clicks it:
// the post is still there, and the card follows it again. A server that took
// the post off, or has another card for it, stays as it is.
func (b *Bot) adoptCard(i *discordgo.Interaction, p lfg.Post) lfg.Post {
	if i.Message == nil || i.GuildID == "" || p.InGuild(i.GuildID) || p.HiddenIn(i.GuildID) {
		return p
	}
	if _, _, ok := findCard(i.Message.Components, p.ID); !ok {
		return p
	}
	stored, ok := b.store.AddMessages(p.ID, []lfg.MessageRef{cardRef(i)})
	if !ok {
		return p
	}
	log.Printf("took back card %s of post %s in guild %s", i.Message.ID, p.ID, i.GuildID)
	if i.Message.Thread != nil {
		b.store.SetThread(p.ID, i.Message.ID)
	}
	b.scheduleSync(p.ID)
	return stored
}

// cardRef is the card that was clicked, with the role it pinged, which stays
// on it as a mention.
func cardRef(i *discordgo.Interaction) lfg.MessageRef {
	ref := lfg.MessageRef{GuildID: i.GuildID, ChannelID: i.ChannelID, MessageID: i.Message.ID}
	for _, c := range i.Message.Components {
		if t, ok := c.(*discordgo.TextDisplay); ok {
			if role, ok := strings.CutPrefix(t.Content, "<@&"); ok {
				ref.PingRoleID = strings.TrimSuffix(role, ">")
			}
		}
	}
	return ref
}

// readCard reads a post off its card, as Discord sent it. It reports false if
// it isn't the post's card, or doesn't say which dungeon it is for or who leads
// it.
func (b *Bot) readCard(comps []discordgo.MessageComponent, postID string) (cardRead, bool) {
	box, row, ok := findCard(comps, postID)
	if !ok {
		return cardRead{}, false
	}
	var texts []string
	for _, c := range box.Components[:row] {
		switch v := c.(type) {
		case *discordgo.TextDisplay:
			texts = append(texts, v.Content)
		case *discordgo.Section:
			for _, c := range v.Components {
				if t, ok := c.(*discordgo.TextDisplay); ok {
					texts = append(texts, t.Content)
				}
			}
		}
	}
	// The heading, the start, then the note if there is one and the roster.
	if len(texts) < 3 {
		return cardRead{}, false
	}
	r := cardRead{post: lfg.Post{ID: postID}}
	if !b.readHeading(texts[0], &r) {
		return cardRead{}, false
	}
	if m := cardStart.FindStringSubmatch(texts[1]); m != nil {
		if at, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			r.post.StartAt = time.Unix(at, 0)
		}
	}
	for _, t := range texts[2:] {
		if note, ok := strings.CutPrefix(t, "> "); ok {
			r.post.Note = unescape(strings.ReplaceAll(note, "\n> ", "\n"))
		} else {
			b.readRoster(t, &r)
		}
	}
	return r, r.leader.UserID != "" || r.leader.Name != ""
}

// readHeading reads the dungeon, the difficulty line if there is one, and how
// the party stands, off the top of a card.
func (b *Bot) readHeading(heading string, r *cardRead) bool {
	lines := strings.Split(heading, "\n")
	title, ok := strings.CutPrefix(lines[0], "## ")
	if !ok || len(lines) < 2 {
		return false
	}
	var dungeon config.Dungeon
	for _, d := range b.game.All() {
		if hasName(title, d.Name) && len(d.Name) > len(dungeon.Name) {
			dungeon = d
		}
	}
	if dungeon.ID == "" {
		return false
	}
	r.post.DungeonID, r.post.Rally = dungeon.ID, dungeon.Abyss
	if len(lines) > 2 {
		b.readDifficulty(lines[1], dungeon, &r.post)
	}
	b.readStatus(lines[len(lines)-1], r)
	return true
}

// readDifficulty reads "**Conquest [Hard]** · GS 2,500+ · ☀️ Elyos · Siel
// server", of which any part may be missing. Only the mode can have " · " in it.
func (b *Bot) readDifficulty(line string, d config.Dungeon, p *lfg.Post) {
	if rest, ok := strings.CutPrefix(line, "**"); ok {
		name, after, _ := strings.Cut(rest, "**")
		for _, m := range d.Modes {
			if m.Name == name {
				p.ModeID = m.ID
			}
		}
		line = strings.TrimPrefix(after, " · ")
	}
	for _, part := range strings.Split(line, " · ") {
		if gs, ok := strings.CutPrefix(part, "GS "); ok {
			p.MinGS, _ = parseGS(gs)
			continue
		}
		for _, f := range b.game.Factions {
			if hasName(part, f.Name) {
				p.Faction = f.ID
			}
		}
		if server, ok := readServer(part); ok && p.Rally {
			p.Server = server
		}
	}
}

// readServer reads a game server's name out of what side.server writes in any
// language.
func readServer(s string) (string, bool) {
	for _, l := range i18n.Langs {
		before, after, _ := strings.Cut(l.T("side.server", "\x00"), "\x00")
		if rest, ok := strings.CutPrefix(s, before); ok {
			if name, ok := strings.CutSuffix(rest, after); ok && name != "" {
				return unescape(name), true
			}
		}
	}
	return "", false
}

// readStatus reads what statusLine writes: how full the party is, and whom it
// is looking for, which a full or ended post doesn't say.
func (b *Bot) readStatus(line string, r *cardRead) {
	head, list, _ := strings.Cut(line, " — ")
	if m := cardFill.FindStringSubmatch(head); m != nil {
		r.filled, _ = strconv.Atoi(m[1])
		r.size, _ = strconv.Atoi(m[2])
	}
	if list != "" {
		if n := cardNumber.FindString(anyCustom.ReplaceAllString(head, "")); n != "" {
			r.missing, _ = strconv.Atoi(n)
		}
	}
	if r.post.Rally {
		r.post.Size = r.size
	}
	for _, item := range strings.Split(list, " · ") {
		if id, ok := b.wantNamed(item); ok {
			r.post.LookingFor = append(r.post.LookingFor, id)
		}
	}
	if len(r.post.LookingFor) == 0 {
		r.post.LookingFor = []string{config.AnyID}
	}
}

// wantNamed finds one "Looking for" entry, as lookingForList wrote it.
func (b *Bot) wantNamed(item string) (string, bool) {
	for _, role := range b.game.Roles {
		if hasName(item, role.Name) {
			return role.ID, true
		}
	}
	for _, c := range b.game.Classes {
		if hasName(item, c.Name) {
			return c.ID, true
		}
	}
	for _, l := range i18n.Langs {
		if hasName(item, l.T("any.name")) {
			return config.AnyID, true
		}
	}
	return "", false
}

// hasName reports whether s is a name, after the emoji that may come first.
func hasName(s, name string) bool { return s == name || strings.HasSuffix(s, " "+name) }

// readRoster reads the sections of rosterText. Their headings differ between
// languages, but not their icons.
func (b *Bot) readRoster(roster string, r *cardRead) {
	list := &r.post.Accepted
	for _, line := range strings.Split(roster, "\n") {
		if strings.HasPrefix(line, "**") {
			switch {
			case strings.Contains(line, "ui_waiting") || strings.Contains(line, "⏳"):
				list = &r.post.Applicants
			case strings.Contains(line, "ui_reserve") || strings.Contains(line, "🪑"):
				list = &r.post.Reserves
			default:
				list = &r.post.Accepted
				if m := cardCount.FindStringSubmatch(line); m != nil {
					r.shown, _ = strconv.Atoi(m[1])
				}
			}
			continue
		}
		if pl, leader, ok := b.readMember(line); leader {
			r.leader = pl
		} else if ok {
			*list = append(*list, pl)
		}
	}
}

// readMember reads a line of the roster, and whether it is the leader's. It
// reports false for any other line.
func (b *Bot) readMember(line string) (pl lfg.Player, leader, ok bool) {
	m := cardMember.FindStringSubmatch(line)
	if m == nil {
		return lfg.Player{}, false, false
	}
	pl = lfg.Player{UserID: m[3], Name: unescape(m[4]), Char: m[5], GuildName: unescape(m[8])}
	for _, token := range m[1:3] {
		if token == "" {
			continue
		}
		if iconID(token) == "leader" || token == b.game.LeaderEmoji {
			leader = true
		} else if role, ok := b.roleOfIcon(token); ok {
			pl.RoleID = role
		}
	}
	if m[6] != "" {
		pl.ClassID = b.classNamed(m[6])
		// Only what the player shows as is on the card. A class that can't
		// fill that role wasn't in it.
		if c, ok := b.game.Class(pl.ClassID); ok && !slices.Contains(c.Roles, pl.RoleID) {
			pl.RoleID = ""
			if len(c.Roles) > 0 {
				pl.RoleID = c.Roles[0]
			}
		}
	}
	pl.GS, _ = parseGS(m[7])
	return pl, leader, true
}

// iconID is the id of an icon, as emoji.Sync names them, or the emoji itself.
func iconID(token string) string {
	m := customEmoji.FindStringSubmatch(token)
	if m == nil {
		return token
	}
	return iconHashPart.ReplaceAllString(strings.TrimPrefix(m[2], "lfg_"), "")
}

func (b *Bot) roleOfIcon(token string) (string, bool) {
	id := iconID(token)
	for _, role := range b.game.Roles {
		if role.ID == id || role.Emoji == token {
			return role.ID, true
		}
	}
	return "", false
}

// classNamed finds a class by the name a roster shows it under. A class no
// longer in the config shows under its ID.
func (b *Bot) classNamed(s string) string {
	for _, c := range b.game.Classes {
		if hasName(s, c.Name) {
			return c.ID
		}
	}
	return s[strings.LastIndex(s, " ")+1:]
}

// findPeople finds out who the people on a card are: the name of those it
// links, and the ID of those it only names. It also works out their home
// servers, which a card shows by name, and leaves out when it's the card's.
func (b *Bot) findPeople(r *cardRead, guildID string) {
	people := []*lfg.Player{&r.leader}
	for _, list := range [][]lfg.Player{r.post.Accepted, r.post.Applicants, r.post.Reserves} {
		for n := range list {
			people = append(people, &list[n])
		}
	}
	forEach(people, func(pl *lfg.Player) {
		homes := []string{guildID}
		if pl.GuildName != "" {
			homes = b.guildsNamed(pl.GuildName)
		}
		if pl.UserID != "" {
			pl.Name = b.nameOf(pl.UserID, append(slices.Clone(homes), guildID)[0])
		} else {
			pl.UserID, homes = b.findByName(pl.Name, homes)
		}
		if len(homes) > 0 {
			pl.GuildID = homes[0]
			pl.GuildName = orDefault(b.guildName(pl.GuildID), pl.GuildName)
		}
	})
}

// guildsNamed finds the servers the bot is on that a roster would show under
// a name.
func (b *Bot) guildsNamed(name string) []string {
	b.s.State.RLock()
	defer b.s.State.RUnlock()
	var ids []string
	for _, g := range b.s.State.Guilds {
		if g.Name != "" && truncate(g.Name, guildTagLength) == name {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

// nameOf is what a user goes by on a server, or anywhere if they left it.
func (b *Bot) nameOf(userID, guildID string) string {
	if m, err := b.s.GuildMember(guildID, userID); err == nil {
		return m.DisplayName()
	}
	if u, err := b.s.User(userID); err == nil {
		return u.DisplayName()
	}
	return userID
}

// findByName finds the one member going by exactly this name on the given
// servers, and which server they are on. Two with the same name can't be told
// apart, so neither is taken for the player.
func (b *Bot) findByName(name string, guildIDs []string) (string, []string) {
	userID, home := "", []string(nil)
	for _, guildID := range guildIDs {
		members, err := b.s.GuildMembersSearch(guildID, name, memberSearchLimit)
		if err != nil {
			log.Printf("look for %s on server %s: %v", name, guildID, err)
			continue
		}
		for _, m := range members {
			if m.User == nil || m.DisplayName() != name {
				continue
			}
			if userID != "" {
				return "", guildIDs
			}
			userID, home = m.User.ID, []string{guildID}
		}
	}
	if userID == "" {
		return "", guildIDs
	}
	return userID, home
}

// restoredPost makes the post that goes back from what a card says and who
// could be found, and names the people who couldn't be. A post that had ended
// is open again, as is one the card still shows open.
func (b *Bot) restoredPost(r cardRead, messageID, guildID string, now time.Time) (lfg.Post, []string) {
	p := r.post
	p.LeaderID, p.LeaderName, p.LeaderChar = r.leader.UserID, r.leader.Name, r.leader.Char
	p.LeaderClass, p.LeaderRole, p.LeaderGS = r.leader.ClassID, r.leader.RoleID, r.leader.GS
	p.GuildID, p.GuildName = r.leader.GuildID, r.leader.GuildName
	if p.GuildID == "" {
		p.GuildID, p.GuildName = guildID, b.guildName(guildID)
	}

	var left []string
	keep := func(list []lfg.Player) []lfg.Player {
		var kept []lfg.Player
		for _, pl := range list {
			switch {
			case pl.UserID == p.LeaderID:
			case pl.UserID == "" || b.blockedFrom(p, pl.UserID, pl.GuildID):
				left = append(left, pl.Name)
			default:
				kept = append(kept, pl)
				p.MarkPinged(pl.UserID)
			}
		}
		return kept
	}
	p.Accepted, p.Applicants, p.Reserves = keep(p.Accepted), keep(p.Applicants), keep(p.Reserves)
	if len(p.Pinged) > 0 {
		p.MarkPinged(p.LeaderID)
	}

	// Places of players who didn't come back open up. Those of players found
	// outside the bot stay taken.
	party := len(p.Party())
	switch {
	case p.Rally:
	case r.size > 0:
		p.Missing = max(0, r.size-r.filled+r.shown-party)
	case r.missing > 0:
		p.Missing = max(0, r.missing+r.shown-party)
	default:
		// An ended post doesn't show how it stood.
		p.Missing = 1
		if size := b.game.DungeonOrUnknown(p.DungeonID).PartySizeFor(p.ModeID); size > 0 {
			p.Missing = max(0, size-party)
		}
	}

	p.CreatedAt = now
	if at, err := discordgo.SnowflakeTimestamp(messageID); err == nil {
		p.CreatedAt = at
	}
	if p.StartAt.After(now) {
		p.ExpiresAt = p.StartAt.Add(b.game.PostTTL())
	}
	// Reopening works out if the party is full, and when it expires otherwise.
	p.Status = lfg.StatusClosed
	p.Reopen(b.game.PostTTL(), now)
	return p, left
}
