package lfg

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

func newPost(missing int) Post {
	return Post{LeaderID: "leader", Missing: missing, Status: StatusOpen, LookingFor: []string{"any"}}
}

func TestJoinAcceptLeave(t *testing.T) {
	p := newPost(2)
	for _, pl := range []Player{{UserID: "a", RoleID: "tank"}, {UserID: "b", RoleID: "healer"}, {UserID: "c", RoleID: "dps"}} {
		if err := p.AddApplicant(pl); err != nil {
			t.Fatalf("join %s: %v", pl.UserID, err)
		}
	}
	if err := p.AddApplicant(Player{UserID: "a"}); !errors.Is(err, ErrAlreadyJoined) {
		t.Fatalf("double join: got %v", err)
	}
	if err := p.AddApplicant(Player{UserID: "leader"}); !errors.Is(err, ErrIsLeader) {
		t.Fatalf("leader join: got %v", err)
	}

	// Only two slots, so the third pick is ignored.
	got, err := p.Accept([]string{"a", "b", "c"})
	if err != nil || len(got) != 2 {
		t.Fatalf("accept: got %d players, err %v", len(got), err)
	}
	if p.Missing != 0 || p.Status != StatusFull || len(p.Applicants) != 1 || p.FilledAt.IsZero() {
		t.Fatalf("after accept: missing %d status %s applicants %d filled %v", p.Missing, p.Status, len(p.Applicants), p.FilledAt)
	}
	// Joining a full party makes a reserve.
	if err := p.AddApplicant(Player{UserID: "d", RoleID: "dps"}); err != nil || !p.IsReserve("d") {
		t.Fatalf("join full post: err %v reserves %v", err, p.Reserves)
	}

	// An accepted player leaving reopens the post when no reserve has
	// their role.
	if r, ok := p.Remove("a"); !ok || !r.WasAccepted || r.Promoted != nil {
		t.Fatalf("remove accepted: ok %v removal %+v", ok, r)
	}
	if p.Missing != 1 || p.Status != StatusOpen || !p.IsReserve("d") {
		t.Fatalf("after leave: missing %d status %s reserves %v", p.Missing, p.Status, p.Reserves)
	}
	if r, ok := p.Remove("c"); !ok || r.WasAccepted || p.Missing != 1 {
		t.Fatalf("remove applicant: ok %v removal %+v missing %d", ok, r, p.Missing)
	}

	// The leader can still pick a reserve for the free slot.
	if got, _ := p.Accept([]string{"d"}); len(got) != 1 || p.Status != StatusFull || len(p.Reserves) != 0 {
		t.Fatalf("accept reserve: got %v status %s reserves %v", got, p.Status, p.Reserves)
	}
}

func TestReserveTakesFreedSlot(t *testing.T) {
	p := newPost(2)
	p.AddApplicant(Player{UserID: "tank", RoleID: "tank"})
	p.AddApplicant(Player{UserID: "healer", RoleID: "healer"})
	p.Accept([]string{"tank", "healer"})
	filled := p.FilledAt
	for _, pl := range []Player{{UserID: "dps1", RoleID: "dps"}, {UserID: "healer2", RoleID: "healer"}, {UserID: "healer3", RoleID: "healer"}} {
		if err := p.AddApplicant(pl); err != nil {
			t.Fatal(err)
		}
	}

	// The first reserve with the leaving player's role moves up; the post
	// stays full.
	r, ok := p.Remove("healer")
	if !ok || r.Promoted == nil || r.Promoted.UserID != "healer2" {
		t.Fatalf("promoted %+v", r.Promoted)
	}
	var reserves []string
	for _, pl := range p.Reserves {
		reserves = append(reserves, pl.UserID)
	}
	if p.Status != StatusFull || p.Missing != 0 || indexOf(p.Accepted, "healer2") < 0 || !slices.Equal(reserves, []string{"dps1", "healer3"}) {
		t.Fatalf("after promotion: status %s missing %d accepted %v reserves %v", p.Status, p.Missing, p.Accepted, reserves)
	}
	if !p.FilledAt.Equal(filled) {
		t.Fatal("filling again must not move the time the party first filled")
	}

	// Reserves count towards the players waiting.
	for n := len(p.Reserves); n < MaxApplicants; n++ {
		p.AddApplicant(Player{UserID: fmt.Sprint("r", n)})
	}
	if err := p.AddApplicant(Player{UserID: "late"}); !errors.Is(err, ErrTooManyJoined) {
		t.Fatalf("join crowded post: got %v", err)
	}
}

func TestStartChangeRemindsAgain(t *testing.T) {
	now := time.Now()
	p := newPost(1)
	p.StartAt, p.Reminded = now.Add(10*time.Minute), true
	p.SetDetails(0, p.StartAt, "note", time.Hour, now)
	if !p.Reminded {
		t.Fatal("the same start must not be reminded twice")
	}
	p.SetDetails(0, now.Add(time.Hour), "note", time.Hour, now)
	if p.Reminded {
		t.Fatal("a new start gets a reminder of its own")
	}
}

func TestGearScoreRequirement(t *testing.T) {
	p := newPost(2)
	p.MinGS = 3000
	if err := p.AddApplicant(Player{UserID: "low", GS: 2999}); !errors.Is(err, ErrGearScore) {
		t.Fatalf("below requirement: got %v", err)
	}
	if err := p.AddApplicant(Player{UserID: "ok", GS: 3000}); err != nil {
		t.Fatalf("at requirement: %v", err)
	}
}

func TestSetDetails(t *testing.T) {
	now := time.Now()
	ttl := 3 * time.Hour
	p := newPost(2)
	p.ExpiresAt = now.Add(time.Hour)
	if err := p.AddApplicant(Player{UserID: "low", GS: 2000}); err != nil {
		t.Fatal(err)
	}

	// Same start: only the gear score and note change; players stay.
	if err := p.SetDetails(3000, time.Time{}, "voice", ttl, now); err != nil {
		t.Fatal(err)
	}
	if p.MinGS != 3000 || p.Note != "voice" || !p.ExpiresAt.Equal(now.Add(time.Hour)) || len(p.Applicants) != 1 {
		t.Fatalf("details only: gs %d note %q expires %v applicants %d", p.MinGS, p.Note, p.ExpiresAt, len(p.Applicants))
	}

	// A later start moves the expiry along with it.
	start := now.Add(48 * time.Hour)
	p.Start = "old free text"
	if err := p.SetDetails(3000, start, "voice", ttl, now); err != nil {
		t.Fatal(err)
	}
	if !p.StartAt.Equal(start) || !p.ExpiresAt.Equal(start.Add(ttl)) || p.Start != "" {
		t.Fatalf("new start: start %v expires %v legacy %q", p.StartAt, p.ExpiresAt, p.Start)
	}

	// Dropping the start leaves the run up for ttl from now.
	if err := p.SetDetails(3000, time.Time{}, "", ttl, now); err != nil {
		t.Fatal(err)
	}
	if !p.StartAt.IsZero() || !p.ExpiresAt.Equal(now.Add(ttl)) {
		t.Fatalf("no start: start %v expires %v", p.StartAt, p.ExpiresAt)
	}

	p.Close(StatusClosed, now)
	if err := p.SetDetails(1, time.Time{}, "", ttl, now); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("edit closed post: got %v", err)
	}
}

func TestPeople(t *testing.T) {
	p := newPost(2)
	p.GuildID = "g1"
	p.AddApplicant(Player{UserID: "a", GuildID: "g2"})
	p.AddApplicant(Player{UserID: "b", GuildID: "g1"})
	p.Accept([]string{"b"})
	p.AdjustMissing(-1, 9)
	p.AddApplicant(Player{UserID: "c", GuildID: "g2"})
	var ids []string
	for _, pl := range p.People() {
		ids = append(ids, pl.UserID+"@"+pl.GuildID)
	}
	if want := []string{"leader@g1", "b@g1", "a@g2", "c@g2"}; !slices.Equal(ids, want) {
		t.Fatalf("people %v, want %v", ids, want)
	}
}

func TestAdjustMissingAndClose(t *testing.T) {
	p := newPost(1)
	if err := p.AdjustMissing(-1, 9); err != nil || p.Status != StatusFull {
		t.Fatalf("-1: err %v status %s", err, p.Status)
	}
	if err := p.AdjustMissing(-1, 9); err != nil || p.Missing != 0 {
		t.Fatalf("below zero: err %v missing %d", err, p.Missing)
	}
	if err := p.AdjustMissing(+20, 9); err != nil || p.Missing != 9 || p.Status != StatusOpen {
		t.Fatalf("above max: err %v missing %d status %s", err, p.Missing, p.Status)
	}
	if err := p.Close(StatusClosed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.AdjustMissing(1, 9); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("adjust closed post: got %v", err)
	}
	if _, err := p.Accept([]string{"x"}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("accept on closed post: got %v", err)
	}
}

func TestMoveKeepsTheParty(t *testing.T) {
	p := newPost(4)
	p.AddApplicant(Player{UserID: "a"})
	p.Accept([]string{"a"})
	p.AddApplicant(Player{UserID: "w"})
	// Leader and a in a party of 5, one more found outside the bot: 2 missing.
	p.AdjustMissing(-1, 4)

	if err := p.Move("draupnir", "hard", 5, 10, 9); err != nil || p.Missing != 7 || p.Status != StatusOpen {
		t.Fatalf("to a raid: err %v missing %d status %s", err, p.Missing, p.Status)
	}
	if p.DungeonID != "draupnir" || p.ModeID != "hard" || len(p.Accepted) != 1 || len(p.Applicants) != 1 {
		t.Fatalf("move lost something: %+v", p)
	}
	if err := p.Move("depot", "easy", 10, 3, 2); err != nil || p.Missing != 0 || p.Status != StatusFull {
		t.Fatalf("to a party of 3: err %v missing %d status %s", err, p.Missing, p.Status)
	}
	if err := p.Move("tiny", "", 3, 2, 1); !errors.Is(err, ErrPartyTooBig) || p.DungeonID != "depot" {
		t.Fatalf("party of 3 into 2: err %v dungeon %s", err, p.DungeonID)
	}
	// Without a known size, only the missing count's cap applies.
	if err := p.Move("unknown", "", 3, 0, 9); err != nil || p.Missing != 0 {
		t.Fatalf("unknown size: err %v missing %d", err, p.Missing)
	}
	p.Close(StatusClosed, time.Now())
	if err := p.Move("krao", "", 0, 5, 4); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("move a closed post: %v", err)
	}
}

func TestReopen(t *testing.T) {
	now := time.Now()
	p := newPost(2)
	p.ExpiresAt = now.Add(time.Hour)
	p.AddApplicant(Player{UserID: "a"})
	p.Accept([]string{"a"})
	if err := p.Reopen(time.Hour, now); !errors.Is(err, ErrStillOpen) {
		t.Fatalf("reopen an open post: %v", err)
	}
	p.Close(StatusClosed, now)
	if err := p.Reopen(3*time.Hour, now); err != nil || p.Status != StatusOpen || !p.ClosedAt.IsZero() ||
		!p.ExpiresAt.Equal(now.Add(time.Hour)) || len(p.Accepted) != 1 {
		t.Fatalf("reopen: err %v %+v", err, p)
	}
	// An expired post gets a fresh expiry; a full one stays full.
	p.AdjustMissing(-1, 9)
	p.Close(StatusExpired, now)
	p.ExpiresAt = now.Add(-time.Minute)
	if err := p.Reopen(3*time.Hour, now); err != nil || p.Status != StatusFull || !p.ExpiresAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("reopen expired: err %v status %s expires %v", err, p.Status, p.ExpiresAt)
	}
}

func TestRallyJoinsStraightIn(t *testing.T) {
	p := Post{LeaderID: "leader", Rally: true, Size: 3, Status: StatusOpen, LookingFor: []string{"any"}}
	for _, pl := range []Player{{UserID: "a", RoleID: "dps"}, {UserID: "b", RoleID: "tank"}, {UserID: "c", RoleID: "healer"}, {UserID: "d", RoleID: "dps"}} {
		if err := p.AddApplicant(pl); err != nil {
			t.Fatalf("join %s: %v", pl.UserID, err)
		}
	}
	// The leader, a and b fill the group of 3 without anyone accepting
	// them; c and d wait as reserves.
	if len(p.Applicants) != 0 || len(p.Accepted) != 2 || len(p.Reserves) != 2 || p.Status != StatusFull || p.FilledAt.IsZero() || p.Missing != 0 {
		t.Fatalf("after joining: %+v", p)
	}
	// The first reserve takes a freed spot, whatever their role.
	if r, ok := p.Remove("a"); !ok || r.Promoted == nil || r.Promoted.UserID != "c" || p.Status != StatusFull {
		t.Fatalf("remove a: %+v, status %s", r, p.Status)
	}
	// A bigger group takes the reserves in; it can't shrink below the
	// players already in it.
	if moved, err := p.SetSize(5); err != nil || len(moved) != 1 || moved[0].UserID != "d" || p.Status != StatusOpen || len(p.Reserves) != 0 {
		t.Fatalf("raise the size: moved %v, err %v, %+v", moved, err, p)
	}
	if _, err := p.SetSize(3); !errors.Is(err, ErrPartyTooBig) || p.Size != 5 {
		t.Fatalf("shrink below the group: err %v, size %d", err, p.Size)
	}
	// Without a limit, the group never fills.
	if _, err := p.SetSize(0); err != nil {
		t.Fatal(err)
	}
	for n := range 40 {
		if err := p.AddApplicant(Player{UserID: fmt.Sprint("p", n)}); err != nil {
			t.Fatalf("join %d: %v", n, err)
		}
	}
	if p.Status != StatusOpen || len(p.Party()) != 44 || p.Crowded() {
		t.Fatalf("unlimited rally: status %s, %d in it", p.Status, len(p.Party()))
	}
	// Changing the event keeps the group as it is.
	if err := p.Move("abyss-boss", "", 5, 2, 9); err != nil || p.DungeonID != "abyss-boss" || p.Size != 0 || p.Missing != 0 {
		t.Fatalf("move: err %v, %+v", err, p)
	}
	if err := p.Close(StatusClosed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetSize(10); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("resize a closed rally: %v", err)
	}
	if err := p.Reopen(time.Hour, time.Now()); err != nil || p.Status != StatusOpen {
		t.Fatalf("reopen: err %v, status %s", err, p.Status)
	}

	dungeon := newPost(2)
	if _, err := dungeon.SetSize(5); !errors.Is(err, ErrNotRally) {
		t.Fatalf("a dungeon post has no group size: %v", err)
	}
}
