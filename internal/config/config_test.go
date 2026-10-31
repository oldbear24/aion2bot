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
