package bot

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// statsPeriods are the periods /lfg-stats offers, in days; the first is
// the default.
var statsPeriods = []int{7, 1, 30}

// statsTop is how many dungeons and servers the stats rank.
const statsTop = 5

// /lfg-stats shows how the runs posted on this server and the servers
// linked with it went.
func (b *Bot) cmdStats(i *discordgo.Interaction) error {
	days := statsPeriods[0]
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "days" && slices.Contains(statsPeriods, int(o.IntValue())) {
			days = int(o.IntValue())
		}
	}
	linked := b.store.Linked(i.GuildID)
	runs := b.store.Runs(linked, time.Now().AddDate(0, 0, -days))
	names := map[string]string{}
	for _, g := range b.store.Guilds() {
		names[g.GuildID] = g.GuildName
	}
	if n, ok := b.store.NetworkOf(i.GuildID); ok {
		for _, m := range n.Members {
			names[m.GuildID] = orDefault(m.GuildName, names[m.GuildID])
		}
	}
	return b.reply(i, b.statsPanel(summarize(runs), days, len(linked), names, lang(i)))
}

// tally counts runs for one dungeon or server; Mode is a dungeon's most
// posted mode.
type tally struct {
	ID   string
	N    int
	Mode string
}

type runStats struct {
	Posted, Open, Filled, Players int
	// FillTime is the median time from posting to a full party.
	FillTime time.Duration
	// Dungeons and Guilds are ranked by runs, most first.
	Dungeons, Guilds []tally
}

func summarize(runs []lfg.Run) runStats {
	var s runStats
	var fills []time.Duration
	dungeons, guilds := map[string]int{}, map[string]int{}
	modes := map[string]map[string]int{}
	for _, r := range runs {
		s.Posted++
		s.Players += r.Players
		if r.Status == lfg.StatusOpen || r.Status == lfg.StatusFull {
			s.Open++
		}
		if !r.FilledAt.IsZero() {
			s.Filled++
			fills = append(fills, max(0, r.FilledAt.Sub(r.CreatedAt)))
		}
		dungeons[r.DungeonID]++
		guilds[r.GuildID]++
		if modes[r.DungeonID] == nil {
			modes[r.DungeonID] = map[string]int{}
		}
		modes[r.DungeonID][r.ModeID]++
	}
	if len(fills) > 0 {
		slices.Sort(fills)
		s.FillTime = fills[len(fills)/2]
	}
	s.Dungeons, s.Guilds = ranked(dungeons), ranked(guilds)
	for n, d := range s.Dungeons {
		if top := ranked(modes[d.ID]); len(top) > 0 {
			s.Dungeons[n].Mode = top[0].ID
		}
	}
	return s
}

// ranked sorts counts by size, then by ID so ties stay put.
func ranked(counts map[string]int) []tally {
	out := make([]tally, 0, len(counts))
	for id, n := range counts {
		out = append(out, tally{ID: id, N: n})
	}
	slices.SortFunc(out, func(a, b tally) int {
		return cmp.Or(cmp.Compare(b.N, a.N), strings.Compare(a.ID, b.ID))
	})
	return out
}

// statsPanel shows the stats of a server and the others linked with it, in
// all servers; names are the servers' names by ID.
func (b *Bot) statsPanel(s runStats, days, servers int, names map[string]string, l i18n.Lang) discordgo.MessageComponent {
	head := l.T("stats.title", l.T("stats.period."+strconv.Itoa(days)))
	if servers > 1 {
		head += "\n" + l.T("stats.network", l.N(servers-1, "n.other_server"))
	} else {
		head += "\n" + l.T("stats.alone")
	}
	if s.Posted == 0 {
		return container(colorFull, text(head), text(l.T("stats.none")))
	}

	lines := []string{
		l.T("stats.posted", s.Posted, s.Open),
		l.T("stats.filled", s.Filled, s.Filled*100/s.Posted),
		l.T("stats.players", s.Players),
	}
	if s.Filled > 0 {
		lines[1] += l.T("stats.fill_time", duration(s.FillTime, l))
	}
	comps := []discordgo.MessageComponent{text(head), text(strings.Join(lines, "\n")), divider()}

	var top strings.Builder
	top.WriteString(l.T("stats.top"))
	for n, t := range s.Dungeons[:min(len(s.Dungeons), statsTop)] {
		d := b.game.DungeonOrUnknown(t.ID)
		fmt.Fprintf(&top, "\n%d. %s **%s** — %s", n+1, d.Emoji, d.Name, l.N(t.N, "n.run"))
		if m, ok := d.Mode(t.Mode); ok && len(d.Modes) > 1 {
			top.WriteString(l.T("stats.mostly", m.Name))
		}
	}
	comps = append(comps, text(top.String()))

	if servers > 1 {
		var by strings.Builder
		by.WriteString(l.T("stats.by_server"))
		for _, t := range s.Guilds[:min(len(s.Guilds), statsTop)] {
			fmt.Fprintf(&by, "\n• %s — %s", escape(truncate(orDefault(names[t.ID], t.ID), 40)), l.N(t.N, "n.run"))
		}
		comps = append(comps, text(by.String()))
	}
	return container(colorFull, comps...)
}

// duration is "18 min" or "2 h 5 min".
func duration(d time.Duration, l i18n.Lang) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return l.T("stats.minutes", max(1, m))
	}
	return l.T("stats.hours", m/60, m%60)
}
