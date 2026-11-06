package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const cannotDM = `{"code":50007,"message":"Cannot send messages to this user"}`

// dmDiscord answers like inviteDiscord, and opens each user a DM channel of
// their own, "dm-<user>". It refuses DMs to the refused users, as Discord
// does to users who don't take DMs from the members of servers they share
// with the bot.
func dmDiscord(refused ...string) *fakeDiscord {
	f := inviteDiscord()
	answer := f.answer
	f.answerSent = func(call, sent string) (int, string) {
		method, path, _ := strings.Cut(call, " ")
		switch {
		case method == "POST" && path == "/users/@me/channels":
			var to struct {
				RecipientID string `json:"recipient_id"`
			}
			json.Unmarshal([]byte(sent), &to)
			return http.StatusOK, fmt.Sprintf(`{"id":"dm-%s","type":1}`, to.RecipientID)
		case method == "POST" && strings.HasPrefix(path, "/channels/dm-"):
			if userID := strings.TrimSuffix(strings.TrimPrefix(path, "/channels/dm-"), "/messages"); slices.Contains(refused, userID) {
				return http.StatusForbidden, cannotDM
			}
		}
		return answer(call)
	}
	return f
}

// pickDMs is a player's pick in the /profile panel's menu for pings.
func pickDMs(t *testing.T, b *Bot, userID, value string) {
	t.Helper()
	b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, userID,
		fmt.Sprintf(`{"custom_id":%q,"component_type":3,"values":[%q]}`, profilePrefix+profDM, value)))
}

// edits lists the bodies the bot edited its answer to the interaction with.
func edits(f *fakeDiscord) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, c := range f.calls {
		if c == "PATCH /webhooks/8/tok/messages/@original" {
			out = append(out, f.bodies[n])
		}
	}
	return out
}

func TestProfileTurnsOnDMsOnceOneGoesThrough(t *testing.T) {
	t.Parallel()
	f := dmDiscord("N")
	b := goneBot(t, f)
	cs := i18n.Czech

	// The first DM says where the pings go now, and the panel answers once
	// it went out.
	pickDMs(t, b, "Y", "on")
	if prof, _ := b.store.Profile("Y"); !prof.ByDM {
		t.Fatalf("DMs aren't on: %+v", prof)
	}
	if dms := sentTo(f, "dm-Y"); len(dms) != 1 || !strings.Contains(dms[0], "Upozornění ti teď budou chodit sem") {
		t.Errorf("the first DM: %q", dms)
	}
	if got := answers(f); len(got) != 1 || got[0] != `{"type":6}` {
		t.Errorf("the panel wasn't deferred: %q", got)
	}
	if got := edits(f); len(got) != 1 || !strings.Contains(got[0], cs.T("profile.dm_saved")) {
		t.Errorf("the panel after the DM: %q", got)
	}

	// Discord refuses DMs to this player: they stay pinged in the thread,
	// and hear why.
	pickDMs(t, b, "N", "on")
	if prof, ok := b.store.Profile("N"); ok && prof.ByDM {
		t.Errorf("DMs on though Discord refused: %+v", prof)
	}
	if got := edits(f); len(got) != 2 || !strings.Contains(got[1], cs.T("profile.dm_refused")) {
		t.Errorf("the panel after a refused DM: %q", got)
	}

	// Back to the thread; no DM says so.
	pickDMs(t, b, "Y", "off")
	if prof, _ := b.store.Profile("Y"); prof.ByDM {
		t.Errorf("DMs still on: %+v", prof)
	}
	if dms := sentTo(f, "dm-Y"); len(dms) != 1 {
		t.Errorf("DMs after turning them off: %q", dms)
	}
	// Picking DMs again while they're on sends no second DM.
	b.saveProfile(lfg.Profile{UserID: "Z", ByDM: true})
	pickDMs(t, b, "Z", "on")
	if dms := sentTo(f, "dm-Z"); len(dms) != 0 {
		t.Errorf("DMs to a player who had them on: %q", dms)
	}
}

// pinged lists whom a note posted in a thread pings.
func pinged(t *testing.T, body string) []string {
	t.Helper()
	var msg struct {
		AllowedMentions struct {
			Users []string `json:"users"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatal(err)
	}
	return msg.AllowedMentions.Users
}

func TestPlayersWhoPickedDMsGetTheirPingsByDM(t *testing.T) {
	t.Parallel()
	// U picked DMs, but Discord refuses them.
	f := dmDiscord("U")
	b := goneBot(t, f)
	p := postWithCard(t, b)
	for _, id := range []string{"L", "U", "V"} {
		b.saveProfile(lfg.Profile{UserID: id, ByDM: true})
	}
	join := func(userID, choice string) {
		t.Helper()
		b.onInteraction(nil, interaction(t, discordgo.InteractionModalSubmit, userID, fmt.Sprintf(`{"custom_id":%q,"components":[
			{"type":18,"component":{"type":3,"custom_id":"choice","values":[%q]}},
			{"type":18,"component":{"type":4,"custom_id":"gs","value":"3100"}}]}`, customID(actJoinForm, p.ID), choice)))
		b.syncWG.Wait()
	}
	accept := func(userID string) {
		t.Helper()
		b.onInteraction(nil, interaction(t, discordgo.InteractionMessageComponent, "L", fmt.Sprintf(`{"custom_id":%q,"component_type":3,
			"values":[%q]}`, customID(actAccept, p.ID), userID)))
		b.syncWG.Wait()
	}

	// The leader hears about the first player by DM, with Manage; the note
	// in the thread pings nobody.
	join("U", "gladiator|tank")
	notes := sentTo(f, "m1")
	if len(notes) != 1 || len(pinged(t, notes[0])) != 0 {
		t.Fatalf("the note in the thread: %q", notes)
	}
	if dms := sentTo(f, "dm-L"); len(dms) != 1 || !strings.Contains(dms[0], customID(actManage, p.ID)) {
		t.Errorf("the leader's DM: %q", dms)
	}

	// Accepted: V by DM, U in the thread, as Discord refused U's DM.
	join("V", "sorcerer|dps")
	accept("V")
	accept("U")
	notes = sentTo(f, "m1")
	if len(notes) != 3 || len(pinged(t, notes[1])) != 0 || !slices.Equal(pinged(t, notes[2]), []string{"U"}) {
		t.Fatalf("the notes in the thread: %q", notes)
	}
	if dms := sentTo(f, "dm-V"); len(dms) != 1 || !strings.Contains(dms[0], "Jsi v partě") {
		t.Errorf("V's DM: %q", dms)
	}
	if dms := sentTo(f, "dm-U"); len(dms) != 1 {
		t.Errorf("U's DM was tried %d times: %q", len(dms), dms)
	}

	// The reminder pings T and U in the thread, and goes to L and V by DM.
	b.remind(p.ID)
	notes = sentTo(f, "m1")
	if len(notes) != 4 || !slices.Equal(pinged(t, notes[3]), []string{"T", "U"}) {
		t.Fatalf("the reminder in the thread: %q", notes)
	}
	for _, id := range []string{"L", "V"} {
		if dms := sentTo(f, "dm-"+id); len(dms) != 2 || !strings.Contains(dms[1], "Je čas se připravit") {
			t.Errorf("%s's reminder: %q", id, dms)
		}
	}
	b.syncWG.Wait()
}

func TestPingsInBatchesGoByDMWhereTheyShould(t *testing.T) {
	t.Parallel()
	// B, C, E and F picked DMs; Discord refuses C's and F's. The post has
	// no copy on g2, so its players are told by DM, if they weren't
	// already.
	f := dmDiscord("C", "F")
	b := goneBot(t, f)
	p := postWithCard(t, b)
	for _, id := range []string{"B", "C", "E", "F"} {
		b.saveProfile(lfg.Profile{UserID: id, ByDM: true})
	}
	var mu sync.Mutex
	var noted [][]string
	note := func(_ string, users []string, first bool, l i18n.Lang) discordgo.MessageComponent {
		mu.Lock()
		defer mu.Unlock()
		noted = append(noted, users)
		return text(fmt.Sprintf("first %v: %v", first, users))
	}
	dm := func(guildID, userID string) bool { return b.dm(userID, text("news from "+guildID)) }

	b.pingInBatches(p, map[string][]string{"g1": {"A", "B", "C"}, "g2": {"D", "E", "F"}}, note, dm)
	notes := sentTo(f, "m1")
	if len(notes) != 1 || !slices.Equal(pinged(t, notes[0]), []string{"A", "C"}) {
		t.Fatalf("the note in the thread: %q", notes)
	}
	// Every DM is tried once: F's isn't tried again for the thread that
	// couldn't take the ping.
	for id, want := range map[string]int{"A": 0, "B": 1, "C": 1, "D": 1, "E": 1, "F": 1} {
		if dms := sentTo(f, "dm-"+id); len(dms) != want {
			t.Errorf("%s's DM was tried %d times, want %d: %q", id, len(dms), want, dms)
		}
	}

	// With every ping gone by DM, the first note still goes to the threads.
	noted = nil
	b.pingInBatches(p, map[string][]string{"g1": {"B"}}, note, dm)
	if notes := sentTo(f, "m1"); len(notes) != 2 || len(pinged(t, notes[1])) != 0 || len(noted) != 1 || len(noted[0]) != 0 {
		t.Errorf("the note with nobody to ping: %q, noted %v", notes, noted)
	}
}
