package bot

import (
	"os"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
)

// testIcons swaps every emoji the bot has an icon for, as in production,
// where each icon is far longer than the emoji it replaces.
var testIcons = func() iconSet {
	uploaded := map[string]string{}
	for _, id := range uiIcons {
		uploaded[iconPrefix+id] = "<:lfg_" + iconPrefix + id + "_a1b2c3:1234567890123456789>"
	}
	return newIconSet(uploaded)
}()

func TestEveryIconIsBundled(t *testing.T) {
	const dir = "../emoji/images/ui/"
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mapped := map[string]bool{}
	for e, id := range uiIcons {
		mapped[id] = true
		if _, err := os.Stat(dir + id + ".png"); err != nil {
			t.Errorf("%s has no icon: %v", e, err)
		}
		// Emoji names have at most 32 characters: "lfg_ui_<id>_1a2b3c".
		if n := len("lfg_" + iconPrefix + id + "_1a2b3c"); n > 32 {
			t.Errorf("icon %s makes a %d character emoji name", id, n)
		}
	}
	for _, f := range files {
		if id := strings.TrimSuffix(f.Name(), ".png"); !mapped[id] {
			t.Errorf("%s isn't used for any emoji", f.Name())
		}
	}
}

// Each role, class and faction in game.json has an icon next to the
// leader's crown, which replaces its emoji.
func TestGameIconsAreBundled(t *testing.T) {
	b := testBot(t)
	var ids []string
	for _, r := range b.game.Roles {
		ids = append(ids, r.ID)
	}
	for _, c := range b.game.Classes {
		ids = append(ids, c.ID)
	}
	for _, f := range b.game.Factions {
		ids = append(ids, f.ID)
	}
	for _, id := range append(ids, "leader") {
		if _, err := os.Stat("../emoji/images/" + id + ".png"); err != nil {
			t.Errorf("%s has no icon: %v", id, err)
		}
		if n := len("lfg_" + id + "_1a2b3c"); n > 32 {
			t.Errorf("icon %s makes a %d character emoji name", id, n)
		}
	}
}

func TestIconsReplaceEmojis(t *testing.T) {
	b := testBot(t)
	join, manage := testIcons.byKey["✋"], testIcons.byKey["⚙️"]
	comps := []discordgo.MessageComponent{
		text("✋ Join · ⚙ Manage · ⚙️ Manage · 🦄 stays"),
		container(colorOpen,
			discordgo.Section{Components: []discordgo.MessageComponent{text("🕒 soon")}, Accessory: manageButton("p", false, i18n.English)},
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{CustomID: "x", Label: "Join", Emoji: emoji("✋")},
				discordgo.Button{CustomID: "y", Label: "Tank", Emoji: emoji(b.roleBullet("tank"))},
			}},
			selectRow("z", "✅ placeholders keep emojis", 1, b.lookingForOptions(nil, i18n.English)),
		),
	}
	got := testIcons.apply(comps)

	if want := join + " Join · " + manage + " Manage · " + manage + " Manage · 🦄 stays"; got[0].(discordgo.TextDisplay).Content != want {
		t.Errorf("text %q, want %q", got[0].(discordgo.TextDisplay).Content, want)
	}
	box := got[1].(discordgo.Container)
	section := box.Components[0].(discordgo.Section)
	if section.Components[0].(discordgo.TextDisplay).Content != testIcons.byKey["🕒"]+" soon" ||
		section.Accessory.(discordgo.Button).Emoji.ID == "" {
		t.Errorf("section %+v", section)
	}
	row := box.Components[1].(discordgo.ActionsRow)
	if e := row.Components[0].(discordgo.Button).Emoji; e.ID != "1234567890123456789" || e.Name != "lfg_ui_join_a1b2c3" {
		t.Errorf("button emoji %+v", e)
	}
	if e := row.Components[1].(discordgo.Button).Emoji; e.Name != b.roleBullet("tank") {
		t.Errorf("a role's own emoji changed: %+v", e)
	}
	menu := box.Components[2].(discordgo.ActionsRow).Components[0].(discordgo.SelectMenu)
	if menu.Placeholder != "✅ placeholders keep emojis" {
		t.Errorf("placeholder %q", menu.Placeholder)
	}
	for _, o := range menu.Options {
		if o.Value == "any" && o.Emoji.ID == "" {
			t.Errorf("Any class keeps its emoji: %+v", o.Emoji)
		}
	}
	// The original components are left alone.
	if comps[0].(discordgo.TextDisplay).Content[:len("✋")] != "✋" {
		t.Error("apply changed its input")
	}

	// Without uploaded icons, nothing changes.
	if out := (iconSet{}).apply(comps); out[0].(discordgo.TextDisplay).Content != comps[0].(discordgo.TextDisplay).Content {
		t.Error("an empty icon set changed a message")
	}
}
