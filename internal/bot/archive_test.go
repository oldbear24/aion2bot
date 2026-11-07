package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const (
	missingPerms   = `{"code":50013,"message":"Missing Permissions"}`
	archivedThread = `{"code":50083,"message":"Thread is archived"}`
	lockedThread   = `{"code":160005,"message":"Thread is locked"}`
)

// withThreads stores a post with its card on g1, g2 and g3 and a thread
// under the first two.
func withThreads(t *testing.T, b *Bot) lfg.Post {
	t.Helper()
	p := postWithCard(t, b)
	// The bot couldn't start a thread under the card on g3.
	b.store.AddMessages(p.ID, []lfg.MessageRef{{GuildID: "g2", ChannelID: "c2", MessageID: "m2"}, {GuildID: "g3", ChannelID: "c3", MessageID: "m3"}})
	b.store.SetThread(p.ID, "m1")
	b.store.SetThread(p.ID, "m2")
	p, ok := b.store.Post(p.ID)
	if !ok {
		t.Fatal("the post wasn't stored")
	}
	return p
}

// archivedThreads lists the threads the bot archived, by ID, each once:
// discordgo itself retries a request Discord fails with a server error.
func archivedThreads(t *testing.T, f *fakeDiscord) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, c := range f.calls {
		threadID, ok := strings.CutPrefix(c, "PATCH /channels/")
		if !ok || strings.Contains(threadID, "/") {
			continue
		}
		var edit struct {
			Archived *bool `json:"archived"`
		}
		if err := json.Unmarshal([]byte(f.bodies[n]), &edit); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if edit.Archived != nil && *edit.Archived {
			out = append(out, threadID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// leaderClicks is the post's leader clicking one of its buttons on the card,
// and the card being brought up to date after.
func leaderClicks(t *testing.T, b *Bot, p lfg.Post, action string) {
	t.Helper()
	card := b.postCard(p, "g1", "", i18n.Czech)
	b.onInteraction(nil, clickAs(click(t, customID(action, p.ID), discordgo.MessageFlagsIsComponentsV2, "cs", card), p.LeaderID, 0))
	b.syncWG.Wait()
}

// The bot archives the threads under a post's copies as soon as it ends:
// when the leader closes it, and in the maintenance round that expires it.
// Reopening the post brings them back, as its note goes to them.
func TestThreadsAreArchivedWhenThePostEnds(t *testing.T) {
	t.Parallel()
	f := &fakeDiscord{answer: func(call string) (int, string) {
		if channel, ok := strings.CutPrefix(call, "POST /channels/"); ok {
			return http.StatusOK, fmt.Sprintf(`{"id":"note-%s"}`, strings.TrimSuffix(channel, "/messages"))
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	p := withThreads(t, b)
	b.archiveEnded()
	if got := archivedThreads(t, f); len(got) != 0 {
		t.Fatalf("archived %v under an open post", got)
	}

	leaderClicks(t, b, p, actClose)
	if p, _ := b.store.Post(p.ID); p.Active() {
		t.Fatalf("the post wasn't closed: %v", f.calls)
	}
	if got := archivedThreads(t, f); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("archived %v, want the threads under the cards m1 and m2", got)
	}
	f.reset()
	b.archiveEnded()
	if len(f.calls) != 0 {
		t.Fatalf("archived again: %v", f.calls)
	}

	leaderClicks(t, b, p, actReopen)
	if p, _ := b.store.Post(p.ID); !p.Active() {
		t.Fatalf("the post wasn't reopened: %v", f.calls)
	}
	for _, thread := range []string{"m1", "m2"} {
		if !f.made("POST /channels/" + thread + "/messages") {
			t.Errorf("no note in the thread %s brings it back: %v", thread, f.calls)
		}
	}

	f.reset()
	if expired := b.store.ExpireDue(time.Now().Add(2 * time.Hour)); !slices.Equal(expired, []string{p.ID}) {
		t.Fatalf("expired %v", expired)
	}
	b.archiveEnded()
	if got := archivedThreads(t, f); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("archived %v once the post expired", got)
	}
}

// A thread that couldn't be archived is tried again in the next maintenance
// round, but only when that might work: not when it's gone, archived already,
// or not the bot's to archive, as a player started it or a moderator locked it.
func TestArchivingTriesAgainOnlyWhenItMightWork(t *testing.T) {
	for _, c := range []struct {
		name     string
		status   int
		body     string
		tryAgain bool
	}{
		{"Discord having trouble", http.StatusInternalServerError, "{}", true},
		{"archived already", http.StatusBadRequest, archivedThread, false},
		{"started by a player", http.StatusForbidden, missingPerms, false},
		{"locked by a moderator", http.StatusBadRequest, lockedThread, false},
		{"deleted", http.StatusNotFound, unknownChan, false},
		{"kept out of the channel", http.StatusForbidden, missingAccess, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			failing := true
			f := &fakeDiscord{answer: func(call string) (int, string) {
				if call == "PATCH /channels/m2" && failing {
					return c.status, c.body
				}
				return http.StatusOK, "{}"
			}}
			b := goneBot(t, f)
			p := withThreads(t, b)
			if _, err := b.store.UpdatePost(p.ID, func(p *lfg.Post) error { return p.Close(lfg.StatusClosed, time.Now()) }); err != nil {
				t.Fatal(err)
			}

			b.archiveEnded()
			if got := archivedThreads(t, f); !slices.Equal(got, []string{"m1", "m2"}) {
				t.Fatalf("archived %v", got)
			}
			failing = false
			f.reset()
			b.archiveEnded()
			var want []string
			if c.tryAgain {
				want = []string{"m1", "m2"}
			}
			if got := archivedThreads(t, f); !slices.Equal(got, want) {
				t.Errorf("the next round archived %v, want %v", got, want)
			}
			f.reset()
			b.archiveEnded()
			if len(f.calls) != 0 {
				t.Errorf("a round after that: %v", f.calls)
			}
		})
	}
}
