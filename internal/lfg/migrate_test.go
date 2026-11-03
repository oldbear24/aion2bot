package lfg

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// openRaw opens a database file without upgrading it.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// v1.5.0 to v1.7.1 ran 15 migrations, the last of them the profile backfill.
// v1.8.0 put three more before it, so a database with that count started at
// the second of them, and never got the first: posts.rally. Every read of a
// post then failed, and the bot said posts no longer existed.
func TestUpgradeThatSkippedAColumnIsRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db := openRaw(t, path)
	mustExec(t, db, schema)
	mustExec(t, db, slices.Concat(migrations[:14], migrations[17:18])...)
	mustExec(t, db, `PRAGMA user_version = 15`,
		`INSERT INTO posts (id, leader_id, leader_name, guild_id, guild_name, dungeon_id, mode_id, min_gs, looking_for, missing, start,
			note, status, created_at, expires_at, closed_at, revision)
			VALUES ('gadrd3', 'L', 'Lea', 'g1', 'One', 'abyssal-forge-ludra', 'conquest', 0, '["tank"]', 1, '', 'a note', 'open', 1, 4102444800000, 0, 7)`,
		`INSERT INTO players (post_id, user_id, name, class_id, role_id, gs, guild_id, guild_name, accepted, position)
			VALUES ('gadrd3', 'T', 'Tom', 'templar', 'tank', 2858, 'g1', 'One', 1, 1)`,
		`INSERT INTO messages (message_id, post_id, guild_id, channel_id, ping_role_id) VALUES ('m1', 'gadrd3', 'g1', 'c1', 'r1')`)

	// What v1.8.0 and later did to it.
	mustExec(t, db, migrations[15:]...)
	mustExec(t, db, `PRAGMA user_version = 23`)
	if _, err := loadPost(db, "gadrd3"); err == nil || !strings.Contains(err.Error(), "no such column: rally") {
		t.Fatalf("this isn't the database from the log: %v", err)
	}
	db.Close()

	// The post was there all along, and is back with all it had.
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.LoadPost("gadrd3")
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if p.Rally || p.Status != StatusOpen || p.Note != "a note" || p.Revision != 7 || len(p.Accepted) != 1 || p.Accepted[0].GS != 2858 ||
			len(p.Messages) != 1 || p.Messages[0].PingRoleID != "r1" {
			t.Errorf("post after the repair: %+v", p)
		}
	}

	// Changes and new posts work again.
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.UpdatePost("gadrd3", func(p *Post) error { p.Note = "changed"; return nil }); err != nil {
		t.Errorf("change a post: %v", err)
	}
	rally := newPost(0)
	rally.Rally, rally.Size = true, 10
	made, err := s.CreatePost(rally, 5)
	if err != nil {
		t.Fatalf("new post: %v", err)
	}
	if got, ok := s.Post(made.ID); !ok || !got.Rally || got.Size != 10 {
		t.Errorf("new rally: %+v", got)
	}
}

// A column goes in whenever it is missing, whatever the database's count says.
func TestAnyMissingColumnComesBack(t *testing.T) {
	s, path := openTestStore(t)
	p, err := s.CreatePost(newPost(2), 5)
	if err != nil {
		t.Fatal(err)
	}
	dropped := 0
	for _, m := range migrations {
		if add := addColumn.FindStringSubmatch(m); add != nil {
			mustExec(t, s.db, "ALTER TABLE "+add[1]+" DROP COLUMN "+add[2])
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("no columns to take away")
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.LoadPost(p.ID); err != nil || got.Missing != 2 {
		t.Errorf("post: %+v, %v", got, err)
	}
	if _, err := s.CreatePost(newPost(1), 5); err != nil {
		t.Errorf("new post: %v", err)
	}
	if err := s.SetProfile(Profile{UserID: "u", Faction: "elyos", Server: "Siel"}); err != nil {
		t.Errorf("profile: %v", err)
	}
	if got, ok := s.Profile("u"); !ok || got.Faction != "elyos" {
		t.Errorf("profile: %+v", got)
	}
}

// What isn't a column, like the profile backfill, still runs once per database.
func TestOtherMigrationsRunOncePerDatabase(t *testing.T) {
	s, path := openTestStore(t)
	s.SetProfile(Profile{UserID: "leader", ClassID: "templar", RoleID: "tank", GS: 3100})
	p, err := s.CreatePost(newPost(2), 5)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Post(p.ID)
	if got.LeaderClass != "" {
		t.Errorf("the backfill ran on a database that had run it: %+v", got.Leader())
	}
	s.Close()

	db := openRaw(t, path)
	mustExec(t, db, `PRAGMA user_version = 15`)
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Post(p.ID); got.LeaderClass != "templar" {
		t.Errorf("the backfill didn't run on a database that hadn't: %+v", got.Leader())
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("user_version %d, %v", version, err)
	}
}

// Databases in use have run these, in this order, and a database runs what
// comes after its count. A new migration goes at the end of the list. Anywhere
// else, those databases never run it, as happened with posts.rally.
func TestMigrationsOnlyGrowAtTheEnd(t *testing.T) {
	released := []string{
		"guilds ADD COLUMN lang", "posts ADD COLUMN start_at", "profiles ADD COLUMN timezone", "messages ADD COLUMN thread",
		"posts ADD COLUMN leader_char", "posts ADD COLUMN filled_at", "posts ADD COLUMN reminded", "players ADD COLUMN char_name",
		"players ADD COLUMN reserve", "profiles ADD COLUMN char_name", "profiles ADD COLUMN skip_reminders",
		"posts ADD COLUMN leader_class", "posts ADD COLUMN leader_role", "posts ADD COLUMN leader_gs",
		"posts ADD COLUMN rally", "posts ADD COLUMN size", "runs ADD COLUMN unlimited", "UPDATE posts SET (leader_class",
		"posts ADD COLUMN faction", "posts ADD COLUMN server", "posts ADD COLUMN hidden", "profiles ADD COLUMN faction", "profiles ADD COLUMN server",
	}
	if len(migrations) < len(released) {
		t.Fatalf("%d migrations, %d released", len(migrations), len(released))
	}
	for n, want := range released {
		if !strings.Contains(migrations[n], want) {
			t.Errorf("migration %d is %q, but released as %q: add new ones at the end", n+1, migrations[n], want)
		}
	}
}
