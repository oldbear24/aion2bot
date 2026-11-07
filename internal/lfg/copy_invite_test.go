package lfg

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func invitePost() Post {
	return Post{ID: "p1", LeaderID: "L", LeaderName: "Lea", GuildID: "g1", DungeonID: "krao-cave", ModeID: "conquest",
		MinGS: 3000, LookingFor: []string{"healer"}, Missing: 2, Status: StatusOpen,
		Applicants: []Player{{UserID: "A", Name: "Ann", ClassID: "cleric", RoleID: "healer", GS: 3100}}}
}

func TestInvite(t *testing.T) {
	p := invitePost()
	for who, want := range map[string]error{"L": ErrIsLeader, "A": ErrAlreadyJoined, "X": nil} {
		if err := p.Invite(Invite{UserID: who, Name: who, GuildID: "g2"}); !errors.Is(err, want) {
			t.Errorf("invite %s: %v, want %v", who, err, want)
		}
	}
	if err := p.Invite(Invite{UserID: "X"}); !errors.Is(err, ErrInvitedAlready) {
		t.Errorf("inviting twice: %v", err)
	}
	if inv, ok := p.InviteOf("X"); !ok || inv.GuildID != "g2" || !p.IsInvited("X") || p.IsInvited("A") {
		t.Errorf("invitation %+v %v", inv, ok)
	}

	// An invited player goes straight into the party, below the gear score
	// and as a class the post isn't looking for, and their invitation is used.
	if err := p.AddApplicant(Player{UserID: "X", ClassID: "gladiator", RoleID: "tank", GS: 100}); err != nil {
		t.Fatal(err)
	}
	if !p.InParty("X") || p.Missing != 1 || p.IsInvited("X") || p.Status != StatusOpen {
		t.Errorf("after the invited player joined: %+v", p)
	}
	// Anyone else still waits for the leader, and needs the gear score.
	if err := p.AddApplicant(Player{UserID: "Y", GS: 100}); !errors.Is(err, ErrGearScore) {
		t.Errorf("an uninvited player below the gear score: %v", err)
	}

	// The last slot goes to the next invited player; once the party is full,
	// an invited player joins as a reserve.
	p.Invite(Invite{UserID: "Y"})
	p.Invite(Invite{UserID: "Z"})
	p.AddApplicant(Player{UserID: "Y"})
	if !p.InParty("Y") || p.Status != StatusFull || p.FilledAt.IsZero() {
		t.Errorf("the last slot: %+v", p)
	}
	if err := p.AddApplicant(Player{UserID: "Z"}); err != nil || !p.IsReserve("Z") || p.IsInvited("Z") {
		t.Errorf("an invited player once the party is full: %v %+v", err, p)
	}

	// Withdrawing.
	p.Invite(Invite{UserID: "W"})
	if !p.Uninvite("W") || p.Uninvite("W") || len(p.Invites) != 0 {
		t.Errorf("withdrawing: %+v", p.Invites)
	}

	// A crowded post still takes an invited player while it has room.
	crowded := invitePost()
	for n := range MaxApplicants - 1 {
		crowded.Applicants = append(crowded.Applicants, Player{UserID: fmt.Sprint(n), GS: 3000})
	}
	crowded.Invite(Invite{UserID: "V"})
	if err := crowded.AddApplicant(Player{UserID: "U", GS: 3000}); !errors.Is(err, ErrTooManyJoined) {
		t.Errorf("an uninvited player on a crowded post: %v", err)
	}
	if !crowded.TakesStraightIn("V") || crowded.TakesStraightIn("U") {
		t.Error("TakesStraightIn")
	}
	if err := crowded.AddApplicant(Player{UserID: "V"}); err != nil || !crowded.InParty("V") {
		t.Errorf("an invited player on a crowded post: %v", err)
	}

	// There's a limit, and none for an ended post.
	many := invitePost()
	for n := range MaxInvites {
		if err := many.Invite(Invite{UserID: fmt.Sprint("i", n)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := many.Invite(Invite{UserID: "one too many"}); !errors.Is(err, ErrTooManyInvites) {
		t.Errorf("past the limit: %v", err)
	}
	many.Close(StatusClosed, time.Now())
	if err := many.Invite(Invite{UserID: "late"}); !errors.Is(err, ErrNotOpen) {
		t.Errorf("an ended post: %v", err)
	}
}

func copyPost() Post {
	return Post{ID: "p1", LeaderID: "L", LeaderName: "Lea", LeaderClass: "cleric", LeaderRole: "healer", GuildID: "g1",
		DungeonID: "krao-cave", ModeID: "conquest", MinGS: 2600, LookingFor: []string{"tank", "dps"}, Missing: 1,
		Faction: "elyos", Note: "old note", Start: "legacy", StartAt: time.Now().Add(-2 * time.Hour), Status: StatusClosed,
		CreatedAt: time.Now().Add(-3 * time.Hour), ClosedAt: time.Now().Add(-time.Hour), FilledAt: time.Now().Add(-150 * time.Minute),
		Revision: 9, Reminded: true, Archived: true,
		Accepted: []Player{{UserID: "T", ClassID: "templar", RoleID: "tank", GuildID: "g1"},
			{UserID: "D", ClassID: "ranger", RoleID: "dps", GuildID: "g2"}},
		Applicants: []Player{{UserID: "A", ClassID: "sorcerer", RoleID: "dps"}},
		Reserves:   []Player{{UserID: "R", ClassID: "gladiator", RoleID: "tank"}},
		Messages:   []MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}},
		Pinged:     []string{"L", "T", "D"}, Hidden: []string{"g3"}, Invites: []Invite{{UserID: "I"}},
	}
}

func ids(players []Player) string {
	s := ""
	for _, pl := range players {
		s += pl.UserID
	}
	return s
}

func TestCopy(t *testing.T) {
	now := time.Now()
	start := now.Add(7 * 24 * time.Hour)
	ttl := 3 * time.Hour
	src := copyPost()

	c := src.Copy(CopyEveryone, start, "new note", ttl, now, nil)
	switch {
	case c.ID != "" || c.Messages != nil || c.Pinged != nil || c.Hidden != nil || c.Invites != nil:
		t.Errorf("the copy took what belongs to the old post: %+v", c)
	case c.Status != StatusOpen || !c.ClosedAt.IsZero() || !c.FilledAt.IsZero() || c.Revision != 0 || c.Reminded || c.Archived:
		t.Errorf("the copy isn't a fresh open post: %+v", c)
	case !c.StartAt.Equal(start) || c.Start != "" || !c.ExpiresAt.Equal(start.Add(ttl)) || !c.CreatedAt.Equal(now) || c.Note != "new note":
		t.Errorf("the copy's times and note: %+v", c)
	case c.LeaderID != "L" || c.LeaderClass != "cleric" || c.DungeonID != "krao-cave" || c.ModeID != "conquest" || c.MinGS != 2600 ||
		c.Faction != "elyos" || len(c.LookingFor) != 2 || c.Missing != 1:
		t.Errorf("the copy isn't the same run: %+v", c)
	case ids(c.Accepted) != "TD" || ids(c.Applicants) != "A" || ids(c.Reserves) != "R":
		t.Errorf("everyone didn't come along in their places: %+v", c)
	}
	// The old post is left as it was.
	c.Accepted[0].Name, c.LookingFor[0] = "changed", "changed"
	if src.Accepted[0].Name != "" || src.LookingFor[0] != "tank" || src.Status != StatusClosed || src.Note != "old note" {
		t.Errorf("copying changed the old post: %+v", src)
	}

	if c := src.Copy(CopyParty, start, "", ttl, now, nil); ids(c.Accepted) != "TD" || len(c.Waiting()) != 0 || c.Missing != 1 {
		t.Errorf("only the party: %+v", c)
	}
	if c := src.Copy(CopyNobody, start, "", ttl, now, nil); len(c.People()) != 1 || c.Missing != 3 {
		t.Errorf("nobody: %+v", c)
	}

	// A player who stays behind leaves as anyone does: the reserve with
	// their role takes their slot.
	c = src.Copy(CopyEveryone, start, "", ttl, now, func(pl Player) bool { return pl.UserID == "T" || pl.UserID == "A" })
	if ids(c.Accepted) != "DR" || len(c.Waiting()) != 0 || c.Missing != 1 {
		t.Errorf("leaving players behind: %+v", c)
	}

	// A full party makes a full copy; a run without a start expires ttl
	// after it was copied.
	src.Missing = 0
	c = src.Copy(CopyEveryone, time.Time{}, "", ttl, now, nil)
	if c.Status != StatusFull || c.FilledAt.IsZero() || !c.ExpiresAt.Equal(now.Add(ttl)) {
		t.Errorf("a full copy: %+v", c)
	}

	// A rally keeps its size, and a reserve moves up into a spot left open.
	rally := Post{LeaderID: "L", Rally: true, Size: 3, Status: StatusFull,
		Accepted: []Player{{UserID: "a"}, {UserID: "b"}}, Reserves: []Player{{UserID: "r"}}}
	c = rally.Copy(CopyEveryone, start, "", ttl, now, func(pl Player) bool { return pl.UserID == "a" })
	if ids(c.Accepted) != "br" || len(c.Reserves) != 0 || c.Size != 3 || c.Status != StatusFull || c.Missing != 0 {
		t.Errorf("a rally: %+v", c)
	}
}

func TestInvitesAreStored(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := invitePost()
	p.Invite(Invite{UserID: "X", Name: "Xena", GuildID: "g2"})
	p, err = s.CreatePost(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Post(p.ID); len(got.Invites) != 1 || got.Invites[0] != (Invite{UserID: "X", Name: "Xena", GuildID: "g2"}) {
		t.Errorf("stored invitations: %+v", got.Invites)
	}
	// Invited players find the post among theirs, until they join.
	if mine := s.PostsOf("X"); len(mine) != 1 || mine[0].ID != p.ID {
		t.Errorf("posts of the invited player: %+v", mine)
	}
	if mine := s.PostsOf("nobody"); len(mine) != 0 {
		t.Errorf("posts of someone else: %+v", mine)
	}
	if _, err := s.UpdatePost(p.ID, func(p *Post) error { return p.AddApplicant(Player{UserID: "X", Name: "Xena"}) }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Post(p.ID)
	if len(got.Invites) != 0 || !got.InParty("X") {
		t.Errorf("after joining: %+v", got)
	}
	if mine := s.PostsOf("X"); len(mine) != 1 {
		t.Errorf("posts of the player who joined: %+v", mine)
	}
}
