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

	"aion2bot/internal/lfg"
)

// slash is a member with Manage Server using a slash command on a server.
func slash(t *testing.T, guildID, name, options string) *discordgo.InteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"9","application_id":"8","type":2,"token":"tok","guild_id":%q,"channel_id":"c1","locale":"en-US",
		"member":{"user":{"id":"admin","username":"admin"},"permissions":"32"},
		"data":{"id":"1","name":%q,"type":1,"resolved":{},"options":%s}}`, guildID, name, options)
	var ic discordgo.InteractionCreate
	if err := json.Unmarshal([]byte(raw), &ic); err != nil {
		t.Fatal(err)
	}
	return &ic
}

// sharedPosts links g1 and g2 in a network and stores a post made on each,
// both with a card and a thread on both: m1 and m2 for the post made on g1,
// n1 and n2 for the one made on g2.
func sharedPosts(t *testing.T, b *Bot) (onOne, onTwo lfg.Post) {
	t.Helper()
	if err := b.store.SetGuild(lfg.GuildConfig{GuildID: "g2", GuildName: "Two", ChannelID: "c2", Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	n, err := b.store.CreateNetwork("Alliance", "g1", "One")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.JoinNetwork(n.Code, "g2", "Two"); err != nil {
		t.Fatal(err)
	}
	return cardsOn(t, b, "g1", "m"), cardsOn(t, b, "g2", "n")
}

// cardsOn stores a post made on a server, with a card and a thread on g1
// and g2: "<prefix>1" and "<prefix>2".
func cardsOn(t *testing.T, b *Bot, guildID, prefix string) lfg.Post {
	t.Helper()
	p, _ := notePost()
	p.GuildID = guildID
	p.CreatedAt, p.ExpiresAt = time.Now(), time.Now().Add(time.Hour)
	p, err := b.store.CreatePost(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	b.store.AddMessages(p.ID, []lfg.MessageRef{{GuildID: "g1", ChannelID: "c1", MessageID: prefix + "1"},
		{GuildID: "g2", ChannelID: "c2", MessageID: prefix + "2"}})
	b.store.SetThread(p.ID, prefix+"1")
	b.store.SetThread(p.ID, prefix+"2")
	return p
}

// cardsOf lists the cards a post still has.
func cardsOf(t *testing.T, b *Bot, postID string) []string {
	t.Helper()
	p, ok := b.store.Post(postID)
	if !ok {
		t.Fatalf("post %s is gone", postID)
	}
	var out []string
	for _, m := range p.Messages {
		out = append(out, m.MessageID)
	}
	slices.Sort(out)
	return out
}

// Servers that no longer share posts don't keep each other's: the cards
// would go on being updated, and their threads relayed, until the posts end.
func TestPostsComeDownWhenServersStopSharingThem(t *testing.T) {
	for _, c := range []struct {
		name, guildID, options string
	}{
		{"a server leaves", "g2", `[{"name":"leave","type":1}]`},
		{"the owner removes a server", "g1", `[{"name":"remove","type":1,"options":[{"name":"server","type":3,"value":"g2"}]}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeDiscord{}
			b := goneBot(t, f)
			onOne, onTwo := sharedPosts(t, b)

			b.onInteraction(nil, slash(t, c.guildID, "lfg-network", c.options))
			if got := cardsOf(t, b, onOne.ID); !slices.Equal(got, []string{"m1"}) {
				t.Errorf("the post made on g1 has the cards %v", got)
			}
			if got := cardsOf(t, b, onTwo.ID); !slices.Equal(got, []string{"n2"}) {
				t.Errorf("the post made on g2 has the cards %v", got)
			}
			for thread, want := range map[string]bool{"m1": false, "m2": true, "n1": true, "n2": false} {
				if f.made("DELETE /channels/"+thread) != want {
					t.Errorf("thread %s deleted: %v, want %v", thread, !want, want)
				}
			}
			for _, id := range []string{onOne.ID, onTwo.ID} {
				if p, _ := b.store.Post(id); !p.Active() {
					t.Errorf("post %s was closed", id)
				}
			}
			if _, ok := b.store.PostByThread("m2"); ok {
				t.Error("the thread on the server that left still belongs to the post")
			}
		})
	}
}

// /lfg-disable takes down what is in the server's channel. Its own posts
// stay up where they are shared; one that is nowhere else is closed.
func TestDisablingTakesTheChannelsPostsDown(t *testing.T) {
	f := &fakeDiscord{answer: func(call string) (int, string) {
		if call == "POST /users/@me/channels" {
			return http.StatusOK, `{"id":"dm"}`
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	onOne, onTwo := sharedPosts(t, b)
	alone, _ := notePost()
	alone.GuildID, alone.CreatedAt, alone.ExpiresAt = "g2", time.Now(), time.Now().Add(time.Hour)
	alone, err := b.store.CreatePost(alone, 10)
	if err != nil {
		t.Fatal(err)
	}
	b.store.AddMessages(alone.ID, []lfg.MessageRef{{GuildID: "g2", ChannelID: "c2", MessageID: "a2"}})

	b.onInteraction(nil, slash(t, "g2", "lfg-disable", `[]`))
	if got := cardsOf(t, b, onOne.ID); !slices.Equal(got, []string{"m1"}) {
		t.Errorf("the post made on g1 has the cards %v", got)
	}
	if got := cardsOf(t, b, onTwo.ID); !slices.Equal(got, []string{"n1"}) {
		t.Errorf("the post made on g2 has the cards %v", got)
	}
	if p, _ := b.store.Post(onTwo.ID); !p.Active() {
		t.Error("the post still shown on g1 was closed")
	}
	if p, _ := b.store.Post(alone.ID); p.Active() || len(p.Messages) != 0 {
		t.Errorf("the post with no card left: status %s, cards %v", p.Status, p.Messages)
	}
	if !f.made("DELETE /channels/c2/messages/a2") || !f.made("POST /channels/dm/messages") {
		t.Errorf("the card wasn't deleted, or the leader wasn't told: %v", f.calls)
	}
}

// A post can't move to another channel, so changing the server's LFG channel
// takes the posts in the old one down and sends them to the new one.
func TestChangingTheChannelMovesThePosts(t *testing.T) {
	sent := 0
	f := &fakeDiscord{answer: func(call string) (int, string) {
		switch call {
		case "GET /channels/c9":
			return http.StatusOK, `{"id":"c9","guild_id":"g1","type":0}`
		case "GET /guilds/g1":
			return http.StatusOK, `{"id":"g1","name":"One","owner_id":"bot"}`
		case "POST /channels/c9/messages":
			sent++
			return http.StatusOK, fmt.Sprintf(`{"id":"new%d"}`, sent)
		}
		return http.StatusOK, "{}"
	}}
	b := goneBot(t, f)
	b.s.State.User = &discordgo.User{ID: "bot"}
	onOne, onTwo := sharedPosts(t, b)
	setup := func() {
		b.onInteraction(nil, slash(t, "g1", "lfg-setup", `[{"name":"channel","type":7,"value":"c9"}]`))
	}

	setup()
	for _, p := range []lfg.Post{onOne, onTwo} {
		got := cardsOf(t, b, p.ID)
		if len(got) != 2 || !strings.HasPrefix(got[1], "new") {
			t.Errorf("post %s has the cards %v, want the one on g2 and a new one", p.ID, got)
		}
		stored, _ := b.store.Post(p.ID)
		if ref, _ := stored.MessageIn("g1"); ref.ChannelID != "c9" {
			t.Errorf("post %s is in channel %q on g1", p.ID, ref.ChannelID)
		}
	}
	if !f.made("DELETE /channels/m1") || !f.made("DELETE /channels/n1") || f.made("DELETE /channels/m2") {
		t.Errorf("the threads in the old channel: %v", f.calls)
	}

	// Setting the same channel again leaves the posts where they are.
	f.reset()
	setup()
	if sent != 2 || f.made("DELETE /channels/m2") {
		t.Errorf("%d posts sent in all, calls: %v", sent, f.calls)
	}
	for n, call := range f.calls {
		if call == callback && strings.Contains(f.bodies[n], "Sending the posts") {
			t.Errorf("the answer announces posts that aren't sent: %s", f.bodies[n])
		}
	}
}
