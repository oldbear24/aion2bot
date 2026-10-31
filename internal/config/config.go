// Package config loads the bot's environment settings and the game data
// (dungeons, roles and classes) offered in the LFG form.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// AnyID is the reserved "Looking for" value meaning anyone is welcome.
const AnyID = "any"

// Discord string selects hold at most 25 options.
const maxSelectOptions = 25

type Dungeon struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji,omitempty"`
	// Image is an optional thumbnail URL shown on the post.
	Image string `json:"image,omitempty"`
	// Description is shown under the dungeon name in the select menu.
	Description string `json:"description,omitempty"`
	// PartySize caps how many players a post for this dungeon can be missing.
	PartySize int `json:"party_size,omitempty"`
	// Modes are the difficulties offered for this dungeon, easiest first.
	Modes []Mode `json:"modes,omitempty"`
}

type Mode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// GS is the gear score the game requires to enter this mode.
	GS int `json:"gs,omitempty"`
	// PartySize overrides the dungeon's party size for this mode.
	PartySize int `json:"party_size,omitempty"`
}

// Role is a party role such as Tank, Healer or DPS. Leaders can look for a
// role instead of specific classes.
type Role struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji,omitempty"`
}

type Class struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji,omitempty"`
	// Roles lists the IDs of the roles this class can fill.
	Roles []string `json:"roles,omitempty"`
	// BackupRoles are roles from Roles the class fills only while nobody
	// else in the party does, like a Chanter healing without a Cleric.
	// Otherwise it shows as its first other role.
	BackupRoles []string `json:"backup_roles,omitempty"`
}

// mainRole is the first role the class fills for real, not as a backup.
func (c Class) mainRole() string {
	for _, r := range c.Roles {
		if !slices.Contains(c.BackupRoles, r) {
			return r
		}
	}
	return ""
}

type Game struct {
	PostTTLHours int `json:"post_ttl_hours"`
	MaxMissing   int `json:"max_missing"`
	// MaxRunsPerLeader caps how many open posts one player can have.
	MaxRunsPerLeader int `json:"max_runs_per_leader"`
	// ReminderMinutes is how long before a planned start the party is
	// pinged.
	ReminderMinutes int       `json:"reminder_minutes"`
	Dungeons        []Dungeon `json:"dungeons"`
	Roles           []Role    `json:"roles"`
	Classes         []Class   `json:"classes"`
}

// Choice is one way a player can join: a class, and the role they fill
// with it (zero when the class has no roles).
type Choice struct {
	Class Class
	Role  Role
}

func LoadGame(path string) (*Game, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read game file: %w", err)
	}
	var g Game
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if g.PostTTLHours <= 0 {
		g.PostTTLHours = 3
	}
	if g.MaxMissing <= 0 {
		g.MaxMissing = 9
	}
	if g.MaxRunsPerLeader <= 0 {
		g.MaxRunsPerLeader = 5
	}
	if g.ReminderMinutes <= 0 {
		g.ReminderMinutes = 15
	}
	g.MaxMissing = min(g.MaxMissing, maxSelectOptions)
	if err := g.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &g, nil
}

func (g *Game) validate() error {
	if len(g.Dungeons) == 0 || len(g.Dungeons) > maxSelectOptions {
		return fmt.Errorf("need between 1 and %d dungeons, got %d", maxSelectOptions, len(g.Dungeons))
	}
	// The "Looking for" menu lists every role, "Any class" and every class.
	if len(g.Classes) == 0 || len(g.Roles)+1+len(g.Classes) > maxSelectOptions {
		return fmt.Errorf("need at least 1 class, and at most %d roles and classes together", maxSelectOptions-1)
	}
	seen := map[string]bool{}
	for _, d := range g.Dungeons {
		if err := checkEntry("dungeon", d.ID, d.Name, seen); err != nil {
			return err
		}
		if d.PartySize == 1 || d.PartySize < 0 {
			return fmt.Errorf("dungeon %q: party_size must be at least 2", d.ID)
		}
		if len(d.Modes) > maxSelectOptions {
			return fmt.Errorf("dungeon %q: at most %d modes", d.ID, maxSelectOptions)
		}
		modes := map[string]bool{}
		for _, m := range d.Modes {
			if err := checkEntry("mode of "+d.ID, m.ID, m.Name, modes); err != nil {
				return err
			}
			if m.PartySize == 1 || m.PartySize < 0 || m.GS < 0 {
				return fmt.Errorf("dungeon %q mode %q: party_size must be at least 2 and gs not negative", d.ID, m.ID)
			}
		}
	}
	// Roles and classes share the "Looking for" menu, so their IDs must not
	// collide.
	seen = map[string]bool{AnyID: true}
	for _, r := range g.Roles {
		if err := checkEntry("role", r.ID, r.Name, seen); err != nil {
			return err
		}
	}
	choices := 0
	for _, c := range g.Classes {
		if err := checkEntry("class", c.ID, c.Name, seen); err != nil {
			return err
		}
		for _, r := range c.Roles {
			if _, ok := g.Role(r); !ok {
				return fmt.Errorf("class %q: unknown role %q", c.ID, r)
			}
		}
		for _, r := range c.BackupRoles {
			if !slices.Contains(c.Roles, r) {
				return fmt.Errorf("class %q: backup role %q isn't one of its roles", c.ID, r)
			}
		}
		if len(c.BackupRoles) > 0 && c.mainRole() == "" {
			return fmt.Errorf("class %q: needs a role besides its backup roles", c.ID)
		}
		choices += max(1, len(c.Roles))
	}
	if choices > maxSelectOptions {
		return fmt.Errorf("classes have %d class/role combinations; the join menu holds at most %d", choices, maxSelectOptions)
	}
	return nil
}

func checkEntry(kind, id, name string, seen map[string]bool) error {
	switch {
	case id == "" || name == "":
		return fmt.Errorf("every %s needs an id and a name", kind)
	case len(id) > 50 || strings.ContainsAny(id, ":|"):
		return fmt.Errorf("%s id %q must be at most 50 characters and contain no ':' or '|'", kind, id)
	case seen[id]:
		return fmt.Errorf("%s id %q is duplicated or reserved", kind, id)
	}
	seen[id] = true
	return nil
}

func (g *Game) PostTTL() time.Duration { return time.Duration(g.PostTTLHours) * time.Hour }

func (g *Game) Reminder() time.Duration { return time.Duration(g.ReminderMinutes) * time.Minute }

func (g *Game) Dungeon(id string) (Dungeon, bool) {
	for _, d := range g.Dungeons {
		if d.ID == id {
			return d, true
		}
	}
	return Dungeon{}, false
}

// DungeonOrUnknown keeps old posts renderable after a dungeon was removed from the config.
func (g *Game) DungeonOrUnknown(id string) Dungeon {
	if d, ok := g.Dungeon(id); ok {
		return d
	}
	return Dungeon{ID: id, Name: id, Emoji: "❔"}
}

func (d Dungeon) Mode(id string) (Mode, bool) {
	for _, m := range d.Modes {
		if m.ID == id {
			return m, true
		}
	}
	return Mode{}, false
}

// PartySizeFor returns the party size of a mode, falling back to the
// dungeon's. Zero means unknown.
func (d Dungeon) PartySizeFor(modeID string) int {
	if m, ok := d.Mode(modeID); ok && m.PartySize > 0 {
		return m.PartySize
	}
	return d.PartySize
}

// MaxMissing is the most players a party for this dungeon and mode can be
// missing: everyone except the leader, and never more than limit.
func (d Dungeon) MaxMissing(modeID string, limit int) int {
	if size := d.PartySizeFor(modeID); size > 1 {
		return min(limit, size-1)
	}
	return limit
}

func (g *Game) Role(id string) (Role, bool) {
	for _, r := range g.Roles {
		if r.ID == id {
			return r, true
		}
	}
	return Role{}, false
}

func (g *Game) Class(id string) (Class, bool) {
	for _, c := range g.Classes {
		if c.ID == id {
			return c, true
		}
	}
	return Class{}, false
}

// UseIcons replaces the emoji of each role and class that has an icon,
// keyed by its id. The emoji from game.json stays for the others.
func (g *Game) UseIcons(icons map[string]string) {
	for i, r := range g.Roles {
		if e, ok := icons[r.ID]; ok {
			g.Roles[i].Emoji = e
		}
	}
	for i, c := range g.Classes {
		if e, ok := icons[c.ID]; ok {
			g.Classes[i].Emoji = e
		}
	}
}

// Backup reports whether a player bringing classID as roleID only stands
// in for that role, and the role they show as instead once someone else
// in the party fills it.
func (g *Game) Backup(classID, roleID string) (instead string, ok bool) {
	c, found := g.Class(classID)
	if !found || !slices.Contains(c.BackupRoles, roleID) {
		return "", false
	}
	return c.mainRole(), true
}

func (g *Game) ClassOrUnknown(id string) Class {
	if c, ok := g.Class(id); ok {
		return c
	}
	return Class{ID: id, Name: id, Emoji: "❔"}
}

// Fits reports whether a player bringing classID as roleID suits a post
// looking for the given roles, classes or anyone.
func (g *Game) Fits(lookingFor []string, classID, roleID string) bool {
	c, ok := g.Class(classID)
	if !ok {
		return false
	}
	if len(c.Roles) == 0 {
		if roleID != "" {
			return false
		}
	} else if !slices.Contains(c.Roles, roleID) {
		return false
	}
	for _, want := range lookingFor {
		if want == AnyID || want == classID || (roleID != "" && want == roleID) {
			return true
		}
	}
	return false
}

// Choices lists every class/role combination that suits a post, grouped by
// role in config order, followed by classes without a role.
func (g *Game) Choices(lookingFor []string) []Choice {
	var out []Choice
	for _, r := range g.Roles {
		for _, c := range g.Classes {
			if slices.Contains(c.Roles, r.ID) && g.Fits(lookingFor, c.ID, r.ID) {
				out = append(out, Choice{Class: c, Role: r})
			}
		}
	}
	for _, c := range g.Classes {
		if len(c.Roles) == 0 && g.Fits(lookingFor, c.ID, "") {
			out = append(out, Choice{Class: c})
		}
	}
	return out
}

// LoadDotEnv sets variables from a KEY=VALUE file without overriding ones
// already present in the environment. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return sc.Err()
}

func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
