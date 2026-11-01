package bot

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/config"
	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

const (
	// findLimit is how many runs /lfg-find lists at most, in findBudget
	// characters.
	findLimit  = 10
	findBudget = 3200
	// idFindJoin is the menu under the list that opens a run's join form.
	idFindJoin = "lfgf:join"
)

// /lfg-find lists the open runs on this server and the servers linked
// with it that the player can join with their class and gear score from
// /profile, soonest first.
func (b *Bot) cmdFind(i *discordgo.Interaction) error {
	userID, _ := interactionUser(i)
	var dungeonID string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "dungeon" {
			dungeonID = o.StringValue()
		}
	}
	prof, _ := b.store.Profile(userID)
	var found []lfg.Post
	for _, p := range b.visiblePosts(i.GuildID) {
		if (dungeonID == "" || p.DungeonID == dungeonID) && b.canJoin(p, userID, prof) {
			found = append(found, p)
		}
	}
	sortRuns(found, time.Now())
	return b.reply(i, b.findPanel(found, i.GuildID, prof, lang(i)))
}

// onFindJoin opens the join form of the run picked under /lfg-find.
func (b *Bot) onFindJoin(i *discordgo.Interaction, values []string) error {
	userID, _ := interactionUser(i)
	p, ok := b.store.Post(first(values))
	if !ok {
		return b.reply(i, notice(colorClosed, errorText(lfg.ErrNotFound, lang(i))))
	}
	return b.onJoin(i, p, userID)
}

// canJoin reports whether a player could join a post: it still takes
// players, they aren't on it yet, and the class and gear score they saved
// in /profile, if any, fit.
func (b *Bot) canJoin(p lfg.Post, userID string, prof lfg.Profile) bool {
	switch {
	case !p.Active() || p.Crowded() || userID == p.LeaderID || p.Has(userID):
		return false
	case prof.GS > 0 && prof.GS < p.MinGS:
		return false
	case prof.ClassID != "" && !b.classFits(p.LookingFor, prof.ClassID):
		return false
	}
	return true
}

// classFits reports whether a class can join a post in any of its roles.
func (b *Bot) classFits(lookingFor []string, classID string) bool {
	return slices.ContainsFunc(b.game.Choices(lookingFor), func(c config.Choice) bool { return c.Class.ID == classID })
}

// sortRuns puts runs with room before full ones, which only take reserves,
// then the soonest start first. A run without a start counts as starting
// now.
func sortRuns(posts []lfg.Post, now time.Time) {
	start := func(p lfg.Post) time.Time {
		if p.StartAt.IsZero() {
			return now
		}
		return p.StartAt
	}
	slices.SortStableFunc(posts, func(x, y lfg.Post) int {
		if xf, yf := x.Status == lfg.StatusFull, y.Status == lfg.StatusFull; xf != yf {
			if xf {
				return 1
			}
			return -1
		}
		return start(x).Compare(start(y))
	})
}

func (b *Bot) findPanel(found []lfg.Post, guildID string, prof lfg.Profile, l i18n.Lang) discordgo.MessageComponent {
	head := l.T("find.title", len(found))
	if prof.ClassID != "" {
		c := b.game.ClassOrUnknown(prof.ClassID)
		basis := strings.TrimSpace(c.Emoji + " " + c.Name)
		if prof.GS > 0 {
			basis += " · GS " + l.GS(prof.GS)
		}
		head += "\n" + l.T("find.matched", basis, b.cmd("profile"))
	} else {
		head += "\n" + l.T("find.no_profile", b.cmd("profile"))
	}
	if len(found) == 0 {
		return container(colorClosed, text(head), text(l.T("find.none", b.cmd("lfg-notify"))))
	}

	var lines strings.Builder
	opts := make([]discordgo.SelectMenuOption, 0, findLimit)
	for n, p := range found[:min(len(found), findLimit)] {
		dungeon := b.game.DungeonOrUnknown(p.DungeonID)
		entry := fmt.Sprintf("\n**%d. %s**", n+1, strings.TrimSpace(dungeon.Emoji+" "+dungeon.Name))
		if line := difficultyLine(dungeon, p.ModeID, p.MinGS, l); line != "" {
			entry += " · " + line
		}
		entry += "\n" + b.statusLine(p, l) + "\n🕒 " + postStart(p, l) + " · " + b.game.LeaderEmoji + " " + escape(truncate(p.LeaderName, 32))
		if ref, ok := b.findCopy(p, guildID); ok {
			entry += " · [" + l.T("find.open") + "](" + messageLink(ref) + ")"
		}
		if utf8.RuneCountInString(lines.String()+entry) > findBudget {
			break
		}
		lines.WriteString(entry)

		desc := l.T("find.option_missing", p.Missing)
		if p.Rally {
			desc = l.T("find.abyss_option_missing", len(p.Party()))
		}
		if p.Status == lfg.StatusFull {
			desc = l.T("find.option_reserve")
		}
		if m, ok := dungeon.Mode(p.ModeID); ok {
			desc = m.Name + " · " + desc
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: truncate(fmt.Sprintf("%d. %s", n+1, dungeon.Name), 100), Value: p.ID,
			Description: truncate(desc+" · "+p.LeaderName, 100), Emoji: emoji(dungeon.Emoji),
		})
	}
	if len(found) > len(opts) {
		lines.WriteString(l.T("find.more", len(found)-len(opts)))
	}
	return container(colorOpen,
		text(head),
		divider(),
		text(strings.TrimSpace(lines.String())),
		selectRow(idFindJoin, l.T("find.join_ph"), 1, opts),
	)
}

// findCopy is the copy of a post to link from /lfg-find: the one on the
// player's server, else the one on the leader's.
func (b *Bot) findCopy(p lfg.Post, guildID string) (lfg.MessageRef, bool) {
	if ref, ok := p.MessageIn(guildID); ok {
		return ref, true
	}
	return p.MessageIn(p.GuildID)
}
