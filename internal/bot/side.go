package bot

import (
	"strings"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// Each AION 2 game server holds one faction, and dungeons match players
// across the servers of a faction, so a post is for one faction. The Abyss
// pairs each server with one of the other faction, so a rally is also for
// one game server. Players say which in /profile, or when they join.

// sameServer compares game server names the way players type them.
func sameServer(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// sideFits reports whether a player of the given faction and game server
// can join a post. What the player didn't say doesn't count against them.
func sideFits(p lfg.Post, faction, server string) bool {
	if p.Faction != "" && faction != "" && faction != p.Faction {
		return false
	}
	return !p.Rally || p.Server == "" || server == "" || sameServer(server, p.Server)
}

// factionTag is "☀️ Elyos", or empty for no or an unknown faction.
func (b *Bot) factionTag(id string) string {
	f, ok := b.game.Faction(id)
	if !ok {
		return ""
	}
	return strings.TrimSpace(f.Emoji + " " + f.Name)
}

// sideLine is whom a post is for: "☀️ Elyos", and the game server of a
// rally, "☀️ Elyos · Siel server". Empty when it doesn't say.
func (b *Bot) sideLine(p lfg.Post, l i18n.Lang) string {
	var parts []string
	if tag := b.factionTag(p.Faction); tag != "" {
		parts = append(parts, tag)
	}
	if p.Rally && p.Server != "" {
		parts = append(parts, l.T("side.server", escape(p.Server)))
	}
	return strings.Join(parts, " · ")
}

// withSide adds whom a post is for to a line such as the difficulty.
func withSide(line, side string) string {
	switch {
	case side == "":
		return line
	case line == "":
		return side
	}
	return line + " · " + side
}

// factionOptions offers the factions, with the selected one pre-selected.
func (b *Bot) factionOptions(selected string) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(b.game.Factions))
	for _, f := range b.game.Factions {
		opts = append(opts, discordgo.SelectMenuOption{Label: f.Name, Value: f.ID, Emoji: emoji(f.Emoji), Default: f.ID == selected})
	}
	return opts
}

// knownFaction keeps a faction only if game.json has it.
func (b *Bot) knownFaction(id string) string {
	if _, ok := b.game.Faction(id); ok {
		return id
	}
	return ""
}

// serverInput asks for the game server, in /profile and the forms.
func serverInput(server, help string, required bool, l i18n.Lang) discordgo.Label {
	return discordgo.Label{
		Label:       l.T("profile.server"),
		Description: truncate(help, 100),
		Component: discordgo.TextInput{CustomID: "server", Style: discordgo.TextInputShort, Value: server,
			Placeholder: l.T("profile.server_ph"), Required: &required, MaxLength: maxChar},
	}
}

// rememberSide fills in the faction and game server a player played on a
// post they led or joined, in their profile, where they'd saved nothing yet.
func (b *Bot) rememberSide(userID, faction, server string) {
	prof, ok := b.store.Profile(userID)
	if !ok {
		prof = lfg.Profile{UserID: userID}
	}
	changed := false
	if prof.Faction == "" && faction != "" {
		prof.Faction, changed = faction, true
	}
	if prof.Server == "" && server != "" {
		prof.Server, changed = server, true
	}
	if changed {
		b.saveProfile(prof)
	}
}
