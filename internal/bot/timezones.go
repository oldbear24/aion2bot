package bot

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no time zone database Go can read.

	"github.com/bwmarrin/discordgo"

	"aion2bot/internal/i18n"
	"aion2bot/internal/lfg"
)

// timeZones are offered in /profile. Each stands for a region sharing one
// clock and is named after its best-known cities in the catalog.
var timeZones = []string{
	"Europe/London", "Europe/Prague", "Europe/Helsinki", "Europe/Moscow",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "America/Sao_Paulo",
	"Asia/Seoul", "Asia/Tokyo", "Australia/Sydney", "UTC",
}

// localeZones guesses a player's time zone from their Discord language
// until they pick one in /profile.
var localeZones = map[string]string{
	"cs": "Europe/Prague", "sk": "Europe/Prague", "pl": "Europe/Prague", "de": "Europe/Prague", "fr": "Europe/Prague",
	"es-ES": "Europe/Prague", "it": "Europe/Prague", "nl": "Europe/Prague", "sv-SE": "Europe/Prague",
	"da": "Europe/Prague", "no": "Europe/Prague", "hu": "Europe/Prague", "hr": "Europe/Prague",
	"fi": "Europe/Helsinki", "ro": "Europe/Helsinki", "bg": "Europe/Helsinki", "el": "Europe/Helsinki",
	"uk": "Europe/Helsinki", "lt": "Europe/Helsinki",
	"ru": "Europe/Moscow", "tr": "Europe/Moscow",
	"en-GB": "Europe/London",
	"en-US": "America/New_York",
	"pt-BR": "America/Sao_Paulo", "es-419": "America/Sao_Paulo",
	"ko": "Asia/Seoul", "ja": "Asia/Tokyo",
}

func guessZone(locale string) string {
	if z, ok := localeZones[locale]; ok {
		return z
	}
	if lang, _, ok := strings.Cut(locale, "-"); ok {
		if z, ok := localeZones[lang]; ok {
			return z
		}
	}
	return "UTC"
}

// timeZone is the zone a player's typed start times are read in: the one
// saved in /profile, else a guess from their Discord language.
func (b *Bot) timeZone(userID string, locale discordgo.Locale) *time.Location {
	name := ""
	if prof, ok := b.store.Profile(userID); ok {
		name = prof.TimeZone
	}
	if name == "" {
		name = guessZone(string(locale))
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

// zoneLabel is "CET (UTC+1)", or just "UTC+5:30" where the zone has no
// common abbreviation.
func zoneLabel(loc *time.Location, now time.Time) string {
	abbr, offset := now.In(loc).Zone()
	utc := "UTC"
	if offset != 0 {
		sign := "+"
		if offset < 0 {
			sign, offset = "-", -offset
		}
		utc += sign + strconv.Itoa(offset/3600)
		if m := offset % 3600 / 60; m != 0 {
			utc += fmt.Sprintf(":%02d", m)
		}
	}
	if abbr == "" || abbr == "UTC" || strings.ContainsAny(abbr[:1], "+-") {
		return utc
	}
	return abbr + " (" + utc + ")"
}

func zoneOptions(selected string, l i18n.Lang, now time.Time) []discordgo.SelectMenuOption {
	opts := make([]discordgo.SelectMenuOption, 0, len(timeZones))
	for _, name := range timeZones {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		opts = append(opts, discordgo.SelectMenuOption{
			Label: l.T("tz." + name), Value: name, Description: zoneLabel(loc, now), Default: name == selected,
		})
	}
	return opts
}

var (
	errBadStart  = errors.New("unreadable start time")
	startPattern = regexp.MustCompile(`^(?:(\d{1,2})\.\s*(\d{1,2})\.?\s*(\d{2}|\d{4})?\s+)?(\d{1,2})[:.](\d{2})$`)
)

// parseStart reads a start time the way players write it, in their time
// zone: "21.09.2026 10:15", "21. 9. 2026 10:15", "21.9. 10:15",
// "2026-09-21 10:15" or just "10:15". A date without a year is this
// year's; a bare time is the next one to come, today or tomorrow. Empty
// means no start time.
func parseStart(s string, loc *time.Location, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	m := startPattern.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, errBadStart
	}
	num := func(i int) int { n, _ := strconv.Atoi(m[i]); return n }
	hour, minute := num(4), num(5)
	if hour > 23 || minute > 59 {
		return time.Time{}, errBadStart
	}

	local := now.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}

	day, month, year := num(1), num(2), local.Year()
	switch {
	case len(m[3]) == 2:
		year = 2000 + num(3)
	case len(m[3]) == 4:
		year = num(3)
	}
	t := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	if t.Day() != day || int(t.Month()) != month { // e.g. 31.02.
		return time.Time{}, errBadStart
	}
	return t, nil
}

// postStart is the start of a post as startText shows it. A rally without
// one gathers right away rather than once the party is full.
func postStart(p lfg.Post, l i18n.Lang) string {
	if p.Rally && p.StartAt.IsZero() && p.Start == "" {
		return l.T("card.abyss_start_default")
	}
	return startText(p.StartAt, p.Start, l)
}

// startText shows a start time as a Discord timestamp, which every reader
// sees in their own time zone, with a countdown. Posts from before the
// start field was a date carry free text instead.
func startText(at time.Time, legacy string, l i18n.Lang) string {
	if !at.IsZero() {
		return fmt.Sprintf("<t:%d:f> · <t:%d:R>", at.Unix(), at.Unix())
	}
	return orDefault(escape(legacy), l.T("card.start_default"))
}
