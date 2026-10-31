package lfg

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func newPost(missing int) Post {
	return Post{LeaderID: "leader", Missing: missing, Status: StatusOpen, LookingFor: []string{"any"}}
}

func TestJoinAcceptLeave(t *testing.T) {
	p := newPost(2)
	for _, id := range []string{"a", "b", "c"} {
		if err := p.AddApplicant(Player{UserID: id}); err != nil {
			t.Fatalf("join %s: %v", id, err)
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
	if p.Missing != 0 || p.Status != StatusFull || len(p.Applicants) != 1 {
		t.Fatalf("after accept: missing %d status %s applicants %d", p.Missing, p.Status, len(p.Applicants))
	}
	if err := p.AddApplicant(Player{UserID: "d"}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("join full post: got %v", err)
	}

	// An accepted player leaving reopens the post.
	if _, wasAccepted, ok := p.Remove("a"); !ok || !wasAccepted {
		t.Fatalf("remove accepted: ok %v wasAccepted %v", ok, wasAccepted)
	}
	if p.Missing != 1 || p.Status != StatusOpen {
		t.Fatalf("after leave: missing %d status %s", p.Missing, p.Status)
	}
	if _, wasAccepted, ok := p.Remove("c"); !ok || wasAccepted || p.Missing != 1 {
		t.Fatalf("remove applicant: ok %v wasAccepted %v missing %d", ok, wasAccepted, p.Missing)
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
	var ids []string
	for _, pl := range p.People() {
		ids = append(ids, pl.UserID+"@"+pl.GuildID)
	}
	if want := []string{"leader@g1", "b@g1", "a@g2"}; !slices.Equal(ids, want) {
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
