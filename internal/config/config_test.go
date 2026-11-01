package config

import (
	"strings"
	"testing"
)

func TestBackupRoles(t *testing.T) {
	g, err := LoadGame("../../game.json")
	if err != nil {
		t.Fatal(err)
	}
	if instead, ok := g.Backup("chanter", "healer"); !ok || instead != "dps" {
		t.Errorf("chanter healing: %q, %v", instead, ok)
	}
	for _, c := range [][2]string{{"chanter", "dps"}, {"cleric", "healer"}, {"nope", "healer"}} {
		if _, ok := g.Backup(c[0], c[1]); ok {
			t.Errorf("%s as %s is no backup", c[0], c[1])
		}
	}

	for _, c := range []struct {
		backup []string
		want   string
	}{
		{[]string{"tank"}, "isn't one of its roles"},
		{[]string{"healer", "dps"}, "besides its backup roles"},
	} {
		bad := *g
		bad.Classes = append([]Class(nil), g.Classes...)
		for i := range bad.Classes {
			if bad.Classes[i].ID == "chanter" {
				bad.Classes[i].BackupRoles = c.backup
			}
		}
		if err := bad.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("backup roles %v: %v", c.backup, err)
		}
	}
}

func TestAbyssEvents(t *testing.T) {
	g, err := LoadGame("../../game.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Abyss) == 0 || len(g.Content(true)) != len(g.Abyss) || len(g.All()) != len(g.Dungeons)+len(g.Abyss) {
		t.Fatalf("abyss events: %d, all %d", len(g.Abyss), len(g.All()))
	}
	for _, d := range g.Abyss {
		if found, ok := g.Dungeon(d.ID); !ok || !found.Abyss {
			t.Errorf("abyss event %s: found %v, %+v", d.ID, ok, found)
		}
	}
	if d, ok := g.Dungeon("krao-cave"); !ok || d.Abyss {
		t.Errorf("Krao Cave: %+v", d)
	}

	for _, c := range []struct {
		name string
		edit func(g *Game)
		want string
	}{
		{"shared id", func(g *Game) { g.Abyss[0].ID = g.Dungeons[0].ID }, "duplicated"},
		{"party size", func(g *Game) { g.Abyss[0].PartySize = 24 }, "no party_size"},
		{"too many", func(g *Game) {
			for len(g.All()) <= maxSelectOptions {
				g.Abyss = append(g.Abyss, Dungeon{ID: "x" + strings.Repeat("x", len(g.Abyss)), Name: "X", Abyss: true})
			}
		}, "together"},
	} {
		bad := *g
		bad.Abyss = append([]Dungeon(nil), g.Abyss...)
		c.edit(&bad)
		if err := bad.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
