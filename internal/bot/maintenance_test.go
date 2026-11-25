package bot

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aion2bot/internal/lfg"
)

// A panic on one of forEach's goroutines, or in a maintenance step, would
// take the bot down: nothing else catches it there.
func TestAPanicInTheBackgroundIsCaught(t *testing.T) {
	var done atomic.Int32
	forEach([]int{1, 2, 3, 4, 5, 6, 7}, func(n int) {
		if n%2 == 0 {
			panic("boom")
		}
		done.Add(1)
	})
	if done.Load() != 4 {
		t.Errorf("%d of the 4 items that don't panic were done", done.Load())
	}

	after := false
	step("testing", func() { panic("boom") })
	step("testing", func() { after = true })
	if !after {
		t.Error("the step after a panic didn't run")
	}
}

// endedPosts stores n closed posts, each with a card "m<i>" on g1, that
// ended one after the other.
func endedPosts(t *testing.T, b *Bot, n int) []lfg.Post {
	t.Helper()
	ended := time.Now().Add(-time.Hour)
	var out []lfg.Post
	for i := range n {
		p, _ := notePost()
		p.CreatedAt, p.ExpiresAt = ended, ended.Add(24*time.Hour)
		p, err := b.store.CreatePost(p, n)
		if err != nil {
			t.Fatal(err)
		}
		b.store.AddMessages(p.ID, []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: fmt.Sprintf("m%d", i)}})
		p, err = b.store.UpdatePost(p.ID, func(p *lfg.Post) error {
			return p.Close(lfg.StatusClosed, ended.Add(time.Duration(i)*time.Second))
		})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// The cleanup takes the posts that ended first, a batch at a time. Posts
// whose cards can't be deleted don't hold up those after them, and are tried
// again later.
func TestPostsThatCantBeDeletedDontHoldUpTheRest(t *testing.T) {
	failing := true
	f := &fakeDiscord{answer: func(call string) (int, string) {
		// The cards of the first batch, m0 to m19.
		if id, ok := strings.CutPrefix(call, "DELETE /channels/c1/messages/m"); ok && failing {
			if n, _ := strconv.Atoi(id); n < deleteBatch {
				return http.StatusForbidden, `{"code":50013,"message":"Missing Permissions"}`
			}
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	posts := endedPosts(t, b, deleteBatch+1)
	last := posts[deleteBatch]
	now, cutoff := time.Now(), time.Now().Add(time.Minute)

	b.deleteEnded(now, cutoff)
	if left := len(b.store.ShownPosts()); left != deleteBatch+1 {
		t.Fatalf("%d posts left after a round in which every delete failed", left)
	}
	f.reset()
	b.deleteEnded(now.Add(time.Minute), cutoff)
	if _, ok := b.store.Post(last.ID); ok {
		t.Errorf("the post after the failing ones wasn't deleted: %v", f.calls)
	}
	if n := len(f.calls); n != 1 {
		t.Errorf("the failing posts were tried again right away: %d calls", n)
	}

	failing = false
	b.deleteEnded(now.Add(cleanupRetry), cutoff)
	if left := b.store.ShownPosts(); len(left) != 0 {
		t.Errorf("%d posts left once their cards could be deleted", len(left))
	}
}
