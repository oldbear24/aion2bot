package bot

import (
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// uiIcons maps the emojis in the bot's messages to its own icons, drawn
// in the style of the role and class icons by tools/icons and bundled in
// internal/emoji/images/ui. Messages are written with the emojis; the
// icons replace them as messages are sent, wherever Discord shows custom
// emojis: in text, and on buttons and menu options. Placeholders, labels
// and modals keep the emojis.
var uiIcons = map[string]string{
	"✋": "join", "🪑": "reserve", "⚙️": "manage", "🕒": "start", "✅": "ok", "⏳": "waiting", "⌛": "expired",
	"🔒": "closed", "🔓": "reopen", "✏️": "edit", "🗺️": "map", "🗑️": "delete", "📣": "post", "🎉": "accepted",
	"⏰": "reminder", "🎮": "ingame", "🎯": "playing", "📈": "gs", "📝": "note", anyEmoji: "any", "❌": "error",
	"⚠️": "warning", "💠": "crystal", "⚓": "anchor", "🏛️": "temple", "🔔": "alert", "🔕": "alert_off",
	"🔎": "find", "📊": "stats", "🔗": "link", "📌": "pin", "🌐": "language", "👥": "players", "🏆": "top",
	"🏠": "server", "⏱️": "timer", "🟢": "open", "📖": "help", "🛠️": "admin", "🔣": "legend", "❔": "unknown",
	"🏰": "castle", "👹": "boss", "🌌": "portal", "🚩": "flag", "💰": "loot", "⛔": "blocked",
}

// iconPrefix marks the interface icons among the uploaded ones, whose ids
// are the file names in images/ui.
const iconPrefix = "ui_"

// variation16 asks for the emoji presentation of a character; the same
// emoji is written with or without it.
const variation16 = "️"

// iconSet swaps emojis for the uploaded icons. The zero value changes
// nothing, so messages keep their emojis when the icons couldn't be
// uploaded.
type iconSet struct {
	text  *strings.Replacer
	byKey map[string]string
}

// newIconSet takes the uploaded icons by id, as emoji.Sync returns them.
func newIconSet(uploaded map[string]string) iconSet {
	set := iconSet{byKey: map[string]string{}}
	var pairs []string
	for e, id := range uiIcons {
		icon, ok := uploaded[iconPrefix+id]
		if !ok {
			continue
		}
		bare := strings.TrimSuffix(e, variation16)
		// The longer spelling goes first, so its variation selector
		// isn't left behind.
		pairs = append(pairs, bare+variation16, icon, bare, icon)
		set.byKey[bare] = icon
		set.byKey[bare+variation16] = icon
	}
	if len(pairs) > 0 {
		set.text = strings.NewReplacer(pairs...)
	}
	return set
}

// apply returns the components with their emojis swapped for icons.
func (ic iconSet) apply(comps []discordgo.MessageComponent) []discordgo.MessageComponent {
	if ic.text == nil {
		return comps
	}
	out := make([]discordgo.MessageComponent, len(comps))
	for i, c := range comps {
		out[i] = ic.component(c)
	}
	return out
}

func (ic iconSet) component(c discordgo.MessageComponent) discordgo.MessageComponent {
	switch v := c.(type) {
	case discordgo.TextDisplay:
		v.Content = ic.text.Replace(v.Content)
		return v
	case discordgo.Container:
		v.Components = ic.apply(v.Components)
		return v
	case discordgo.Section:
		v.Components = ic.apply(v.Components)
		v.Accessory = ic.component(v.Accessory)
		return v
	case discordgo.ActionsRow:
		v.Components = ic.apply(v.Components)
		return v
	case discordgo.Button:
		v.Emoji = ic.emoji(v.Emoji)
		return v
	case discordgo.SelectMenu:
		v.Options = slices.Clone(v.Options)
		for i := range v.Options {
			v.Options[i].Emoji = ic.emoji(v.Options[i].Emoji)
		}
		return v
	}
	return c
}

func (ic iconSet) emoji(e *discordgo.ComponentEmoji) *discordgo.ComponentEmoji {
	if e == nil || e.ID != "" {
		return e
	}
	if icon, ok := ic.byKey[e.Name]; ok {
		return emoji(icon)
	}
	return e
}
