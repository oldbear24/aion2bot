package lfg

import (
	"errors"
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

	// Players keep their order and their accepted state.
	p, err = s.UpdatePost(p.ID, func(p *Post) error {
		for _, id := range []string{"a", "b", "c"} {
			if err := p.AddApplicant(Player{UserID: id, ClassID: "cleric", RoleID: "healer", GS: 3000, GuildID: "g1"}); err != nil {
				return err
			}
		}
		_, err := p.Accept([]string{"b"})
		p.MarkPinged("b")
		return err
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
	case len(got.Accepted) != 1 || got.Accepted[0].UserID != "b" || got.Missing != 2:
		t.Fatalf("accepted %v missing %d", got.Accepted, got.Missing)
	case len(got.Applicants) != 2 || got.Applicants[0].UserID != "a" || got.Applicants[1].GS != 3000:
		t.Fatalf("applicants %v", got.Applicants)
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
	want := Profile{UserID: "u1", ClassID: "gladiator", RoleID: "tank", GS: 2900, TimeZone: "Europe/Prague"}
	if err := s.SetProfile(want); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Profile("u1"); !ok || got != want {
		t.Fatalf("got %+v", got)
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
