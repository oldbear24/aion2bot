package i18n

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var verb = regexp.MustCompile(`%(\[\d+\])?[sdv]`)

func TestCatalogsMatch(t *testing.T) {
	for key, en := range en {
		cs, ok := cs[key]
		if !ok {
			t.Errorf("%s: missing in Czech", key)
			continue
		}
		if a, b := len(verb.FindAllString(en, -1)), len(verb.FindAllString(cs, -1)); a != b {
			t.Errorf("%s: English has %d placeholders, Czech %d", key, a, b)
		}
	}
	for key := range cs {
		if _, ok := en[key]; !ok && !strings.HasSuffix(key, ".few") {
			t.Errorf("%s: missing in English", key)
		}
	}
}

func TestDiscordLimits(t *testing.T) {
	for _, l := range Langs {
		for key, s := range catalogs[l] {
			limit := 0
			switch {
			case strings.HasPrefix(key, "cmd."):
				limit = 100 // command and option descriptions
			case strings.HasSuffix(key, "_ph"):
				limit = 150 // placeholders
			case key == "card.join" || key == "card.leave" || key == "card.manage" || strings.HasPrefix(key, "manage.") &&
				!strings.Contains(key, "title") && !strings.Contains(key, "footer") && !strings.Contains(key, "looking") &&
				!strings.Contains(key, "full") && !strings.Contains(key, "ended") && !strings.Contains(key, "disappears") &&
				!strings.Contains(key, "only_leader"):
				limit = 80 // button labels and select placeholders
			}
			if limit > 0 && utf8.RuneCountInString(s) > limit {
				t.Errorf("%s %s: %d characters, Discord allows %d", l, key, utf8.RuneCountInString(s), limit)
			}
		}
	}
}

func TestPluralsAndNumbers(t *testing.T) {
	for _, c := range []struct {
		l    Lang
		n    int
		want string
	}{
		{English, 1, "1 player"}, {English, 3, "3 players"},
		{Czech, 1, "1 hráč"}, {Czech, 3, "3 hráči"}, {Czech, 5, "5 hráčů"}, {Czech, 0, "0 hráčů"},
	} {
		if got := c.l.N(c.n, "n.player"); got != c.want {
			t.Errorf("%s N(%d) = %q, want %q", c.l, c.n, got, c.want)
		}
	}
	if English.GS(4500) != "4,500" || Czech.GS(4500) != "4"+nbsp+"500" || Czech.GS(800) != "800" {
		t.Errorf("GS: %q %q", English.GS(4500), Czech.GS(4500))
	}
	if Parse("cs") != Czech || Parse("en-US") != English || Parse("de") != English || Parse("") != English {
		t.Error("Parse")
	}
	if Czech.T("dm.removed", "Lea", "Krao Cave") != "Už nejsi v partě do **Krao Cave** (vedoucí: **Lea**)." {
		t.Errorf("reordered arguments: %s", Czech.T("dm.removed", "Lea", "Krao Cave"))
	}
}
