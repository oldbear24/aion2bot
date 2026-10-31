// Package lfg holds the raid "looking for group" posts and the per-server
// settings, and persists them to SQLite.
package lfg

import (
	"errors"
	"slices"
	"time"
)

type Status string

const (
	StatusOpen    Status = "open"
	StatusFull    Status = "full"
	StatusClosed  Status = "closed"
	StatusExpired Status = "expired"
)

// MaxApplicants keeps the leader's accept menu within Discord's 25-option limit.
const MaxApplicants = 25

var (
	ErrNotFound      = errors.New("post not found")
	ErrNotOpen       = errors.New("post is not open")
	ErrIsLeader      = errors.New("user is the post leader")
	ErrAlreadyJoined = errors.New("user already joined")
	ErrNotInParty    = errors.New("user is not on this post")
	ErrTooManyJoined = errors.New("too many applicants")
	ErrTooManyPosts  = errors.New("leader has too many open posts")
	ErrGearScore     = errors.New("gear score below the post's requirement")
)

// GuildConfig is where a server wants LFG posts delivered.
type GuildConfig struct {
	GuildID    string `json:"guild_id"`
	GuildName  string `json:"guild_name"`
	ChannelID  string `json:"channel_id"`
	PingRoleID string `json:"ping_role_id,omitempty"`
	// Lang is the language of the posts in this server's channel.
	Lang string `json:"lang,omitempty"`
}

type Player struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	ClassID   string `json:"class_id"`
	RoleID    string `json:"role_id,omitempty"`
	GS        int    `json:"gs,omitempty"`
	GuildID   string `json:"guild_id,omitempty"`
	GuildName string `json:"guild_name,omitempty"`
	// Char is the player's in-game character name, for the invite.
	Char string `json:"char,omitempty"`
}

// MessageRef is one copy of a post in one server's LFG channel.
type MessageRef struct {
	GuildID    string `json:"guild_id"`
	ChannelID  string `json:"channel_id"`
	MessageID  string `json:"message_id"`
	PingRoleID string `json:"ping_role_id,omitempty"`
	// Thread is set when the bot started a thread under the card. Discord
	// gives such a thread the ID of the message it was started from.
	Thread bool `json:"thread,omitempty"`
}

// Webhook is a channel webhook the bot created; it can post in the
// channel's threads under any name and avatar.
type Webhook struct {
	ID    string
	Token string
}

// Relay is a copy of a player's thread message in the thread on another
// server. Webhook is zero when the bot posted the copy itself.
type Relay struct {
	ChannelID string
	MessageID string
	Webhook   Webhook
}

type Post struct {
	ID         string `json:"id"`
	LeaderID   string `json:"leader_id"`
	LeaderName string `json:"leader_name"`
	// LeaderChar is the leader's in-game character name.
	LeaderChar string `json:"leader_char,omitempty"`
	// LeaderClass, LeaderRole and LeaderGS are what the leader plays in
	// the party; the class is empty when they didn't say.
	LeaderClass string `json:"leader_class_id,omitempty"`
	LeaderRole  string `json:"leader_role_id,omitempty"`
	LeaderGS    int    `json:"leader_gs,omitempty"`
	GuildID     string `json:"guild_id"`
	GuildName   string `json:"guild_name"`
	DungeonID   string `json:"dungeon_id"`
	ModeID      string `json:"mode_id,omitempty"`
	MinGS       int    `json:"min_gs,omitempty"`
	// LookingFor holds role IDs, class IDs, or just "any". It keeps its old
	// JSON name so posts saved before roles existed still load.
	LookingFor []string `json:"class_ids"`
	Missing    int      `json:"missing"`
	// Start is free text from posts made before the start time picker.
	Start string `json:"start,omitempty"`
	// StartAt is when the run starts; zero means as soon as the party is
	// full.
	StartAt   time.Time `json:"start_at"`
	Note      string    `json:"note,omitempty"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	ClosedAt  time.Time `json:"closed_at"`
	// Revision increases on every change so callers can tell whether a
	// rendered copy is stale.
	Revision   int      `json:"revision"`
	Applicants []Player `json:"applicants"`
	Accepted   []Player `json:"accepted"`
	// Reserves joined while the party was full. When a party member
	// leaves, the first reserve with their role takes the slot.
	Reserves []Player     `json:"reserves,omitempty"`
	Messages []MessageRef `json:"messages"`
	// Pinged lists users already notified about this post; later changes
	// only edit the card silently.
	Pinged []string `json:"pinged,omitempty"`
	// FilledAt is when the party was first full, for the stats.
	FilledAt time.Time `json:"filled_at"`
	// Reminded is set once the reminder before the start is due, whether
	// or not anyone was reminded.
	Reminded bool `json:"reminded,omitempty"`
}

func (p *Post) Active() bool { return p.Status == StatusOpen || p.Status == StatusFull }

func (p *Post) Clone() Post {
	c := *p
	c.LookingFor = slices.Clone(p.LookingFor)
	c.Applicants = slices.Clone(p.Applicants)
	c.Accepted = slices.Clone(p.Accepted)
	c.Reserves = slices.Clone(p.Reserves)
	c.Messages = slices.Clone(p.Messages)
	c.Pinged = slices.Clone(p.Pinged)
	return c
}

// MarkPinged records that a user was notified about this post and reports
// whether this is the first time.
func (p *Post) MarkPinged(userID string) bool {
	if slices.Contains(p.Pinged, userID) {
		return false
	}
	p.Pinged = append(p.Pinged, userID)
	return true
}

// MessageIn returns the copy of the post in a server's LFG channel.
func (p *Post) MessageIn(guildID string) (MessageRef, bool) {
	for _, m := range p.Messages {
		if m.GuildID == guildID {
			return m, true
		}
	}
	return MessageRef{}, false
}

// People is everyone on the post: the party, the players waiting and
// the reserves.
func (p *Post) People() []Player { return slices.Concat(p.Party(), p.Applicants, p.Reserves) }

// Party is the leader followed by the players they accepted.
func (p *Post) Party() []Player { return slices.Concat([]Player{p.Leader()}, p.Accepted) }

// Leader is the post's leader as a player.
func (p *Post) Leader() Player {
	return Player{UserID: p.LeaderID, Name: p.LeaderName, ClassID: p.LeaderClass, RoleID: p.LeaderRole, GS: p.LeaderGS,
		GuildID: p.GuildID, GuildName: p.GuildName, Char: p.LeaderChar}
}

func (p *Post) Has(userID string) bool {
	return indexOf(p.Applicants, userID) >= 0 || indexOf(p.Accepted, userID) >= 0 || p.IsReserve(userID)
}

func (p *Post) IsReserve(userID string) bool { return indexOf(p.Reserves, userID) >= 0 }

func (p *Post) InParty(userID string) bool { return indexOf(p.Accepted, userID) >= 0 }

// Waiting is everyone the leader can still accept: the applicants, then
// the reserves.
func (p *Post) Waiting() []Player { return slices.Concat(p.Applicants, p.Reserves) }

// Crowded reports whether too many players are waiting for anyone else to
// join.
func (p *Post) Crowded() bool { return len(p.Applicants)+len(p.Reserves) >= MaxApplicants }

// GuildIDs lists the servers that have a copy of the post.
func (p *Post) GuildIDs() []string {
	ids := make([]string, 0, len(p.Messages))
	for _, m := range p.Messages {
		ids = append(ids, m.GuildID)
	}
	return ids
}

func (p *Post) InGuild(guildID string) bool {
	return slices.ContainsFunc(p.Messages, func(m MessageRef) bool { return m.GuildID == guildID })
}

// AddApplicant puts a player on the post: on the list for the leader while
// the party has room, or among the reserves once it is full.
func (p *Post) AddApplicant(pl Player) error {
	switch {
	case !p.Active():
		return ErrNotOpen
	case pl.UserID == p.LeaderID:
		return ErrIsLeader
	case p.Has(pl.UserID):
		return ErrAlreadyJoined
	case p.Crowded():
		return ErrTooManyJoined
	case pl.GS < p.MinGS:
		return ErrGearScore
	}
	if p.Status == StatusFull {
		p.Reserves = append(p.Reserves, pl)
	} else {
		p.Applicants = append(p.Applicants, pl)
	}
	return nil
}

// Accept moves applicants or reserves into the party until no slots are
// left and returns the players that were actually accepted.
func (p *Post) Accept(userIDs []string) ([]Player, error) {
	if !p.Active() {
		return nil, ErrNotOpen
	}
	var accepted []Player
	for _, id := range userIDs {
		if p.Missing == 0 {
			break
		}
		pl, ok := p.takeWaiting(id)
		if !ok {
			continue
		}
		p.Accepted = append(p.Accepted, pl)
		p.Missing--
		accepted = append(accepted, pl)
	}
	p.refreshStatus()
	return accepted, nil
}

// Removal is a player taken off a post.
type Removal struct {
	Player      Player
	WasAccepted bool
	// Promoted is the reserve who took the freed slot in the party.
	Promoted *Player
}

// Remove drops a player from the post. An accepted player's slot goes to
// the first reserve with the same role; without one, it opens again.
func (p *Post) Remove(userID string) (Removal, bool) {
	if pl, ok := p.takeWaiting(userID); ok {
		return Removal{Player: pl}, true
	}
	i := indexOf(p.Accepted, userID)
	if i < 0 {
		return Removal{}, false
	}
	r := Removal{Player: p.Accepted[i], WasAccepted: true}
	p.Accepted = slices.Delete(p.Accepted, i, i+1)
	if j := slices.IndexFunc(p.Reserves, func(res Player) bool { return res.RoleID == r.Player.RoleID }); j >= 0 {
		promoted := p.Reserves[j]
		p.Reserves = slices.Delete(p.Reserves, j, j+1)
		p.Accepted = append(p.Accepted, promoted)
		r.Promoted = &promoted
	} else {
		p.Missing++
	}
	p.refreshStatus()
	return r, true
}

// takeWaiting removes a player from the applicants or the reserves.
func (p *Post) takeWaiting(userID string) (Player, bool) {
	for _, list := range []*[]Player{&p.Applicants, &p.Reserves} {
		if i := indexOf(*list, userID); i >= 0 {
			pl := (*list)[i]
			*list = slices.Delete(*list, i, i+1)
			return pl, true
		}
	}
	return Player{}, false
}

// AdjustMissing lets the leader account for players found outside the bot.
func (p *Post) AdjustMissing(delta, maxMissing int) error {
	if !p.Active() {
		return ErrNotOpen
	}
	p.Missing = max(0, min(maxMissing, p.Missing+delta))
	p.refreshStatus()
	return nil
}

// SetDetails changes the minimum gear score, start and note of an active
// post. Players already on it stay, whatever their gear score. A new start
// moves the expiry to ttl after it, or after now for a run without one,
// and gets a reminder of its own.
func (p *Post) SetDetails(minGS int, startAt time.Time, note string, ttl time.Duration, now time.Time) error {
	if !p.Active() {
		return ErrNotOpen
	}
	p.MinGS, p.Note = minGS, note
	if !startAt.Equal(p.StartAt) {
		p.StartAt, p.Start, p.Reminded = startAt, "", false
		p.ExpiresAt = now.Add(ttl)
		if startAt.After(now) {
			p.ExpiresAt = startAt.Add(ttl)
		}
	}
	return nil
}

func (p *Post) Close(status Status, now time.Time) error {
	if !p.Active() {
		return ErrNotOpen
	}
	p.Status = status
	p.ClosedAt = now
	return nil
}

func (p *Post) refreshStatus() {
	if !p.Active() {
		return
	}
	if p.Missing == 0 {
		p.Status = StatusFull
		if p.FilledAt.IsZero() {
			p.FilledAt = time.Now()
		}
	} else {
		p.Status = StatusOpen
	}
}

func indexOf(players []Player, userID string) int {
	return slices.IndexFunc(players, func(pl Player) bool { return pl.UserID == userID })
}
