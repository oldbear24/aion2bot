package bot

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"aion2bot/internal/lfg"
)

// renamed waits for the renames of a post to finish and returns the names
// its threads were given, as "thread: name", in the order Discord took them.
func renamed(t *testing.T, b *Bot, f *fakeDiscord, postID string) []string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.renameMu.Lock()
		_, running := b.renames[postID]
		b.renameMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rename never finished")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, call := range f.calls {
		if thread, ok := strings.CutPrefix(call, "PATCH /channels/"); ok && !strings.Contains(thread, "/") {
			_, name, _ := strings.Cut(f.bodies[n], `"name":"`)
			name, _, _ = strings.Cut(name, `"`)
			out = append(out, thread+": "+name)
		}
	}
	return out
}

// Discord makes the third rename of a thread in ten minutes wait. The bot
// waits with that thread alone, and gives it the name the post has by then;
// a rename asked for meanwhile doesn't start a second one next to it.
func TestARenameToldToWaitTakesTheNameThePostHasByThen(t *testing.T) {
	limited := true
	f := &fakeDiscord{answer: func(call string) (int, string) {
		if call == "PATCH /channels/m1" && limited {
			limited = false
			return http.StatusTooManyRequests, `{"message":"You are being rate limited.","retry_after":0.3,"global":false}`
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	b.stop = make(chan struct{})
	p := withThreads(t, b)

	b.scheduleRename(p.ID)
	for !f.made("PATCH /channels/m2") {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := b.store.UpdatePost(p.ID, func(p *lfg.Post) error { p.LeaderName = "Tom"; return nil }); err != nil {
		t.Fatal(err)
	}
	b.scheduleRename(p.ID)
	b.scheduleRename(p.ID)

	got := renamed(t, b, f, p.ID)
	if len(got) != 4 {
		t.Fatalf("renames: %v", got)
	}
	first, second := got[:2], got[2:]
	slices.Sort(first)
	slices.Sort(second)
	// The thread told to wait took no name the first time.
	if want := []string{"m1: Krao Cave · Conquest · Lea", "m2: Krao Cave · Conquest · Lea"}; !slices.Equal(first, want) {
		t.Errorf("the first renames: %v, want %v", first, want)
	}
	if want := []string{"m1: Krao Cave · Conquest · Tom", "m2: Krao Cave · Conquest · Tom"}; !slices.Equal(second, want) {
		t.Errorf("the renames after the wait: %v, want %v", second, want)
	}
}

// Without another change, only the thread that was told to wait is renamed
// again, as every rename counts towards Discord's two in ten minutes.
func TestOnlyTheThreadToldToWaitIsRenamedAgain(t *testing.T) {
	limited := true
	f := &fakeDiscord{answer: func(call string) (int, string) {
		if call == "PATCH /channels/m1" && limited {
			limited = false
			return http.StatusTooManyRequests, `{"message":"You are being rate limited.","retry_after":0.1,"global":false}`
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	b.stop = make(chan struct{})
	p := withThreads(t, b)

	b.scheduleRename(p.ID)
	got := renamed(t, b, f, p.ID)
	slices.Sort(got)
	want := []string{"m1: Krao Cave · Conquest · Lea", "m1: Krao Cave · Conquest · Lea", "m2: Krao Cave · Conquest · Lea"}
	if !slices.Equal(got, want) {
		t.Errorf("renames: %v, want %v", got, want)
	}
}
