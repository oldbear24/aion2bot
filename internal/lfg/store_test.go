package lfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestStorePostsRoundTrip(t *testing.T) {
	s, path := openTestStore(t)
	if err := s.SetGuild(GuildConfig{GuildID: "g1", GuildName: "One", ChannelID: "c1"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	p := newPost(3)
	p.LookingFor = []string{"tank", "cleric"}
	p.ExpiresAt = now.Add(time.Hour)
	p.StartAt = time.Date(2026, 11, 1, 19, 30, 0, 0, time.UTC)
	p, err := s.CreatePost(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePost(newPost(1), 2); err != nil {
		t.Fatalf("second post is within the limit: %v", err)
	}
	if _, err := s.CreatePost(newPost(1), 2); !errors.Is(err, ErrTooManyPosts) {
		t.Fatalf("third post over the limit of 2: got %v", err)
	}

	// A failed update must not leave a half-applied change behind.
	_, err = s.UpdatePost(p.ID, func(p *Post) error {
		p.Missing = 99
		return ErrNotOpen
	})
	if got, _ := s.Post(p.ID); !errors.Is(err, ErrNotOpen) || got.Missing != 3 {
		t.Fatalf("rolled back update: err %v missing %d", err, got.Missing)
	}

	// Players keep their order, their state and their characters.
	p, err = s.UpdatePost(p.ID, func(p *Post) error {
		p.LeaderChar, p.LeaderClass, p.LeaderRole, p.LeaderGS = "Lea", "templar", "tank", 3100
		for _, id := range []string{"a", "b", "c"} {
			if err := p.AddApplicant(Player{UserID: id, ClassID: "cleric", RoleID: "healer", GS: 3000, GuildID: "g1", Char: "Char" + id}); err != nil {
				return err
			}
		}
		_, err := p.Accept([]string{"b"})
		p.MarkPinged("b")
		p.AdjustMissing(-2, 9)
		return errors.Join(err, p.AddApplicant(Player{UserID: "d", ClassID: "ranger", RoleID: "dps"}))
	})
	if err != nil {
		t.Fatal(err)
	}

	s.AddMessages(p.ID, []MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}})
	// The thread under the card has the card's ID.
	s.SetThread(p.ID, "m1")
	s.AddReply(p.ID, MessageRef{GuildID: "g1", ChannelID: "m1", MessageID: "r1"})
	s.SetThread(p.ID, "r1")
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.Post(p.ID)
	switch {
	case !ok:
		t.Fatal("post lost after reopening")
	case len(got.Messages) != 1:
		t.Fatalf("replies must not count as copies of the card: %v", got.Messages)
	case !got.Messages[0].Thread:
		t.Fatalf("the card's thread was lost: %+v", got.Messages[0])
	case len(got.Accepted) != 1 || got.Accepted[0].UserID != "b" || got.Missing != 0:
		t.Fatalf("accepted %v missing %d", got.Accepted, got.Missing)
	case len(got.Applicants) != 2 || got.Applicants[0].UserID != "a" || got.Applicants[1].GS != 3000 || got.Applicants[1].Char != "Charc":
		t.Fatalf("applicants %v", got.Applicants)
	case len(got.Reserves) != 1 || got.Reserves[0].UserID != "d":
		t.Fatalf("reserves %v", got.Reserves)
	case got.LeaderChar != "Lea" || got.FilledAt.UnixMilli() != p.FilledAt.UnixMilli() || got.FilledAt.IsZero():
		t.Fatalf("leader character %q, filled %v", got.LeaderChar, got.FilledAt)
	case got.Leader() != (Player{UserID: p.LeaderID, Name: p.LeaderName, ClassID: "templar", RoleID: "tank", GS: 3100,
		GuildID: p.GuildID, GuildName: p.GuildName, Char: "Lea"}):
		t.Fatalf("leader %+v", got.Leader())
	case !slices.Equal(got.LookingFor, []string{"tank", "cleric"}) || !slices.Equal(got.Pinged, []string{"b"}):
		t.Fatalf("looking for %v, pinged %v", got.LookingFor, got.Pinged)
	case !got.StartAt.Equal(p.StartAt):
		t.Fatalf("start %v, want %v", got.StartAt, p.StartAt)
	case got.Revision != p.Revision:
		t.Fatalf("revision %d, want %d", got.Revision, p.Revision)
	}

	for _, m := range reopened.AllMessages(p.ID) {
		if m.Thread != (m.MessageID == "m1") {
			t.Errorf("only the card has a thread: %+v", m)
		}
	}
}

func TestThreadsRelaysAndWebhooks(t *testing.T) {
	s, _ := openTestStore(t)
	p, err := s.CreatePost(newPost(2), 5)
	if err != nil {
		t.Fatal(err)
	}
	s.AddMessages(p.ID, []MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}, {GuildID: "g2", ChannelID: "c2", MessageID: "m2"}})
	s.AddReply(p.ID, MessageRef{GuildID: "g1", ChannelID: "m1", MessageID: "r1"})

	// A thread has its card's ID; replies and unknown channels aren't posts.
	if got, ok := s.PostByThread("m2"); !ok || got.ID != p.ID {
		t.Fatalf("thread m2: ok %v post %q", ok, got.ID)
	}
	if _, ok := s.PostByThread("r1"); ok {
		t.Fatal("a reply is not a card")
	}
	if _, ok := s.PostByThread("elsewhere"); ok {
		t.Fatal("unknown channel found a post")
	}

	hook := Webhook{ID: "w2", Token: "secret"}
	s.AddRelays(p.ID, "src", []Relay{{ChannelID: "m2", MessageID: "copy2", Webhook: hook}, {ChannelID: "m3", MessageID: "copy3"}})
	got := s.Relays("src")
	slices.SortFunc(got, func(a, b Relay) int { return strings.Compare(a.MessageID, b.MessageID) })
	if len(got) != 2 || got[0].Webhook != hook || got[1].Webhook != (Webhook{}) || got[1].ChannelID != "m3" {
		t.Fatalf("relays %+v", got)
	}
	s.RemoveRelays("src")
	if got := s.Relays("src"); len(got) != 0 {
		t.Fatalf("relays left after removing: %+v", got)
	}

	// Relays go with their post.
	s.AddRelays(p.ID, "src2", []Relay{{ChannelID: "m2", MessageID: "copy4"}})
	s.DeletePost(p.ID)
	if got := s.Relays("src2"); len(got) != 0 {
		t.Fatalf("relays outlived their post: %+v", got)
	}

	if _, ok := s.Webhook("c1"); ok {
		t.Fatal("no webhook was set")
	}
	s.SetWebhook("c1", hook)
	if w, ok := s.Webhook("c1"); !ok || w != hook {
		t.Fatalf("webhook %+v ok %v", w, ok)
	}
	s.RemoveWebhook("c1")
	if _, ok := s.Webhook("c1"); ok {
		t.Fatal("webhook left after removing")
	}
}

func TestExpireAndDeleteEnded(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	p := newPost(3)
	p.ExpiresAt = now.Add(time.Hour)
	p, _ = s.CreatePost(p, 5)
	s.AddMessages(p.ID, []MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: "m1"}})
	s.AddReply(p.ID, MessageRef{GuildID: "g1", ChannelID: "c1", MessageID: "r1"})

	if ids := s.ExpireDue(now.Add(2 * time.Hour)); !slices.Equal(ids, []string{p.ID}) {
		t.Fatalf("expire: got %v", ids)
	}
	if len(s.ActivePostsByLeader("leader")) != 0 {
		t.Fatal("expired post is still active")
	}
	if ended := s.EndedBefore(now.Add(time.Hour), 10); len(ended) != 0 {
		t.Fatalf("post ended less than a day ago is due already: %v", ended)
	}

	ended := s.EndedBefore(now.Add(3*time.Hour), 10)
	if len(ended) != 1 || len(ended[0].Messages) != 2 {
		t.Fatalf("ended posts with card and reply: %+v", ended)
	}
	s.DeletePost(p.ID)
	if _, ok := s.Post(p.ID); ok {
		t.Fatal("deleted post still stored")
	}
}

func TestRemindersAreDueOnce(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	window := 15 * time.Minute
	create := func(start time.Time) Post {
		p := newPost(2)
		p.StartAt, p.ExpiresAt = start, now.Add(5*time.Hour)
		p, err := s.CreatePost(p, 10)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	soon := create(now.Add(10 * time.Minute))
	create(now.Add(time.Hour))
	create(now.Add(-time.Minute)) // already started
	create(time.Time{})           // starts when full
	closed := create(now.Add(5 * time.Minute))
	s.UpdatePost(closed.ID, func(p *Post) error { return p.Close(StatusClosed, now) })

	if ids := s.DueReminders(window, now); !slices.Equal(ids, []string{soon.ID}) {
		t.Fatalf("due: %v, want %v", ids, soon.ID)
	}
	if ids := s.DueReminders(window, now); len(ids) != 0 {
		t.Fatalf("reminded twice: %v", ids)
	}
	// Marking it doesn't count as a change, and later changes keep it.
	p, _ := s.UpdatePost(soon.ID, func(p *Post) error { return p.AdjustMissing(-1, 9) })
	if !p.Reminded || p.Revision != soon.Revision+1 {
		t.Fatalf("reminded %v revision %d", p.Reminded, p.Revision)
	}
}

func TestRunsKeepEndedPosts(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	create := func(guildID, dungeonID string) Post {
		p := newPost(1)
		p.GuildID, p.DungeonID, p.CreatedAt, p.ExpiresAt = guildID, dungeonID, now, now.Add(time.Hour)
		p, err := s.CreatePost(p, 10)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	filled := create("g1", "krao")
	s.UpdatePost(filled.ID, func(p *Post) error {
		p.AddApplicant(Player{UserID: "a"})
		p.Accept([]string{"a"})
		return p.Close(StatusClosed, now)
	})
	expired := create("g2", "fire")
	open := create("g1", "fire")
	create("elsewhere", "krao")
	s.UpdatePost(open.ID, func(p *Post) error { return p.AddApplicant(Player{UserID: "b"}) })
	s.ExpireDue(now.Add(30 * time.Minute)) // nothing yet
	s.UpdatePost(expired.ID, func(p *Post) error { p.ExpiresAt = now.Add(-time.Second); return nil })
	s.ExpireDue(now)

	// Deleting a post keeps its run.
	s.DeletePost(filled.ID)
	s.DeletePost(expired.ID)

	runs := s.Runs([]string{"g1", "g2"}, now.Add(-time.Hour))
	got := map[Status]Run{}
	for _, r := range runs {
		got[r.Status] = r
	}
	switch {
	case len(runs) != 3:
		t.Fatalf("runs %+v", runs)
	case got[StatusClosed].DungeonID != "krao" || got[StatusClosed].FilledAt.IsZero() || got[StatusClosed].Players != 1:
		t.Fatalf("closed run %+v", got[StatusClosed])
	case got[StatusExpired].GuildID != "g2" || !got[StatusExpired].FilledAt.IsZero():
		t.Fatalf("expired run %+v", got[StatusExpired])
	case got[StatusOpen].Players != 1:
		t.Fatalf("open run %+v", got[StatusOpen])
	}
	if runs := s.Runs([]string{"g1", "g2"}, now.Add(time.Hour)); len(runs) != 0 {
		t.Fatalf("runs from before the period: %+v", runs)
	}
	s.PruneRuns(now.Add(time.Hour))
	if runs := s.Runs([]string{"g1", "g2"}, now.Add(-time.Hour)); len(runs) != 1 || runs[0].Status != StatusOpen {
		t.Fatalf("after pruning: %+v", runs)
	}
}

func TestAlerts(t *testing.T) {
	s, _ := openTestStore(t)
	if _, ok := s.Alert("u1"); ok {
		t.Fatal("alert before saving")
	}
	want := Alert{UserID: "u1", GuildID: "g1", Lang: "cs", Dungeons: []string{"krao", "fire"}, Choices: []string{"cleric|healer"}}
	s.SetAlert(want)
	s.SetAlert(Alert{UserID: "u2", GuildID: "g2", Dungeons: []string{"krao"}})
	got, ok := s.Alert("u1")
	if !ok || got.GuildID != "g1" || got.Lang != "cs" || !slices.Equal(got.Dungeons, want.Dungeons) || !slices.Equal(got.Choices, want.Choices) || !got.On() {
		t.Fatalf("got %+v", got)
	}
	if u2, _ := s.Alert("u2"); u2.On() || u2.Lang != "en" || u2.Choices == nil {
		t.Fatalf("alert without choices: %+v", u2)
	}
	if in := s.AlertsIn([]string{"g1", "g3"}); len(in) != 1 || in[0].UserID != "u1" {
		t.Fatalf("alerts in g1: %+v", in)
	}
	s.RemoveAlert("u1")
	if _, ok := s.Alert("u1"); ok {
		t.Fatal("alert left after removing")
	}
}

func TestNetworksDecideWherePostsGo(t *testing.T) {
	s, _ := openTestStore(t)
	for _, g := range []string{"a", "b", "c"} {
		s.SetGuild(GuildConfig{GuildID: g, GuildName: "Server " + g, ChannelID: "ch-" + g})
	}
	targets := func(g string) []string {
		var ids []string
		for _, cfg := range s.Targets(g) {
			ids = append(ids, cfg.GuildID)
		}
		slices.Sort(ids)
		return ids
	}

	if got := targets("a"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("unlinked server should only post to itself, got %v", got)
	}

	n, err := s.CreateNetwork("Alliance", "a", "Server a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNetwork("Other", "a", "Server a"); !errors.Is(err, ErrInNetwork) {
		t.Fatalf("second network for the same server: %v", err)
	}
	if _, err := s.JoinNetwork("WRONG", "b", "Server b"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("bad code: %v", err)
	}
	if _, err := s.JoinNetwork(n.Code, "b", "Server b"); err != nil {
		t.Fatal(err)
	}
	if got := targets("a"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("linked servers share posts, got %v", got)
	}
	if got := targets("c"); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("server outside the network got %v", got)
	}

	// Only the owner manages the network.
	if _, err := s.NewNetworkCode("b"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner changed the code: %v", err)
	}
	renewed, err := s.NewNetworkCode("a")
	if err != nil || renewed.Code == n.Code {
		t.Fatalf("new code: %v, %q", err, renewed.Code)
	}
	if _, err := s.JoinNetwork(n.Code, "c", "Server c"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("old code still works: %v", err)
	}
	if _, err := s.RemoveFromNetwork("b", "a"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner removed a server: %v", err)
	}

	// The owner leaving hands the network to the next server.
	if _, err := s.LeaveNetwork("a"); err != nil {
		t.Fatal(err)
	}
	left, ok := s.NetworkOf("b")
	if !ok || left.OwnerGuildID != "b" || len(left.Members) != 1 {
		t.Fatalf("after owner left: %+v", left)
	}
	if _, err := s.LeaveNetwork("b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.NetworkOf("b"); ok {
		t.Fatal("empty network should be gone")
	}
}

func TestProfiles(t *testing.T) {
	s, _ := openTestStore(t)
	if _, ok := s.Profile("u1"); ok {
		t.Fatal("profile before saving")
	}
	want := Profile{UserID: "u1", ClassID: "gladiator", RoleID: "tank", GS: 2900, TimeZone: "Europe/Prague", Char: "Lea", SkipReminders: true}
	if err := s.SetProfile(want); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Profile("u1"); !ok || got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestOlderPostsTakeTheLeaderFromTheProfile(t *testing.T) {
	s, path := openTestStore(t)
	s.SetProfile(Profile{UserID: "leader", ClassID: "templar", RoleID: "tank", GS: 3100})
	old, err := s.CreatePost(newPost(2), 9)
	if err != nil {
		t.Fatal(err)
	}
	picked := newPost(2)
	picked.LeaderClass, picked.LeaderRole = "cleric", "healer"
	if picked, err = s.CreatePost(picked, 9); err != nil {
		t.Fatal(err)
	}
	// Go back to before the profile backfill.
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)-1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, _ := reopened.Post(old.ID); got.LeaderClass != "templar" || got.LeaderRole != "tank" || got.LeaderGS != 3100 {
		t.Errorf("older post's leader: %+v", got.Leader())
	}
	if got, _ := reopened.Post(picked.ID); got.LeaderClass != "cleric" || got.LeaderRole != "healer" {
		t.Errorf("the leader's own pick was replaced: %+v", got.Leader())
	}
}

// Versions before SQLite kept everything in store.json and shared posts
// with every server.
const legacyJSON = `{
  "guilds": {
    "g1": {"guild_id": "g1", "guild_name": "One", "channel_id": "c1"},
    "g2": {"guild_id": "g2", "guild_name": "Two", "channel_id": "c2", "ping_role_id": "r2"}
  },
  "posts": {
    "abc123": {"id": "abc123", "leader_id": "L", "leader_name": "Leader", "guild_id": "g1", "dungeon_id": "krao-cave",
      "class_ids": ["any"], "missing": 2, "status": "open", "created_at": "2026-10-30T19:27:46+01:00",
      "expires_at": "2026-10-30T22:27:46+01:00", "closed_at": "0001-01-01T00:00:00Z", "revision": 3,
      "applicants": [{"user_id": "p1", "name": "Ann", "class_id": "cleric"}], "accepted": null,
      "messages": [{"guild_id": "g1", "channel_id": "c1", "message_id": "m1"}, {"guild_id": "g2", "channel_id": "c2", "message_id": "m2"}]}
  }
}`

func TestImportJSON(t *testing.T) {
	s, path := openTestStore(t)
	legacy := filepath.Join(filepath.Dir(path), "store.json")
	if err := os.WriteFile(legacy, []byte(legacyJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if imported, err := s.ImportJSON(legacy); err != nil || !imported {
		t.Fatalf("import: %v %v", imported, err)
	}
	if _, err := os.Stat(legacy + ".imported"); err != nil {
		t.Fatal("legacy file should be renamed after import")
	}
	if imported, _ := s.ImportJSON(legacy); imported {
		t.Fatal("imported twice")
	}

	p, ok := s.Post("abc123")
	if !ok || len(p.Messages) != 2 || len(p.Applicants) != 1 || !slices.Equal(p.LookingFor, []string{"any"}) || !p.ClosedAt.IsZero() {
		t.Fatalf("imported post: %+v", p)
	}
	if n, ok := s.NetworkOf("g2"); !ok || len(n.Members) != 2 {
		t.Fatalf("imported servers should stay linked: %+v", n)
	}
}

func TestReopenPost(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	create := func() Post {
		p := newPost(1)
		p.GuildID, p.CreatedAt, p.ExpiresAt = "g1", now, now.Add(time.Hour)
		p, err := s.CreatePost(p, 2)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	closed := create()
	s.UpdatePost(closed.ID, func(p *Post) error { return p.Close(StatusClosed, now) })
	if runs := s.Runs([]string{"g1"}, now.Add(-time.Hour)); len(runs) != 1 || runs[0].Status != StatusClosed {
		t.Fatalf("closed run %+v", runs)
	}

	// Two open posts are the most here, so the closed one can't come back.
	other := create()
	create()
	if _, err := s.ReopenPost(closed.ID, 2, time.Hour, now); !errors.Is(err, ErrTooManyPosts) {
		t.Fatalf("reopen past the limit: %v", err)
	}
	s.UpdatePost(other.ID, func(p *Post) error { return p.Close(StatusClosed, now) })
	p, err := s.ReopenPost(closed.ID, 2, time.Hour, now)
	if err != nil || p.Status != StatusOpen {
		t.Fatalf("reopen: err %v status %s", err, p.Status)
	}
	// It counts once, as open, not also as ended.
	statuses := map[Status]int{}
	for _, r := range s.Runs([]string{"g1"}, now.Add(-time.Hour)) {
		statuses[r.Status]++
	}
	if statuses[StatusOpen] != 2 || statuses[StatusClosed] != 1 {
		t.Fatalf("runs after reopening: %v", statuses)
	}
	if _, err := s.ReopenPost(closed.ID, 5, time.Hour, now); !errors.Is(err, ErrStillOpen) {
		t.Fatalf("reopen twice: %v", err)
	}
}

func TestShownPostsIncludeEndedOnes(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	var ids []string
	for range 3 {
		p := newPost(1)
		p.CreatedAt, p.ExpiresAt = now, now.Add(time.Hour)
		p, err := s.CreatePost(p, 10)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	s.UpdatePost(ids[1], func(p *Post) error { return p.Close(StatusClosed, now) })
	s.DeletePost(ids[2])

	var shown []string
	for _, p := range s.ShownPosts() {
		shown = append(shown, p.ID)
	}
	if len(shown) != 2 || !slices.Contains(shown, ids[0]) || !slices.Contains(shown, ids[1]) {
		t.Fatalf("shown %v, want the open and the closed post of %v", shown, ids)
	}
}

func TestRalliesRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	now := time.Now()
	create := func(size int) Post {
		p, err := s.CreatePost(Post{LeaderID: "L", GuildID: "g1", DungeonID: "artifact-siege", Rally: true, Size: size,
			Status: StatusOpen, LookingFor: []string{"any"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, 10)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	capped, unlimited := create(5), create(0)
	p, err := s.UpdatePost(capped.ID, func(p *Post) error { return p.AddApplicant(Player{UserID: "a"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Post(p.ID); !got.Rally || got.Size != 5 || len(got.Accepted) != 1 || len(got.Applicants) != 0 {
		t.Fatalf("stored rally %+v", got)
	}
	for _, p := range []Post{capped, unlimited} {
		s.UpdatePost(p.ID, func(p *Post) error { return p.Close(StatusClosed, now) })
	}
	runs := s.Runs([]string{"g1"}, now.Add(-time.Hour))
	unlimitedRuns := 0
	for _, r := range runs {
		if r.Unlimited {
			unlimitedRuns++
		}
	}
	if len(runs) != 2 || unlimitedRuns != 1 {
		t.Fatalf("runs %+v", runs)
	}
}
