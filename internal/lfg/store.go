package lfg

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store keeps server settings, posts and player profiles in SQLite. It is
// safe for concurrent use; every getter returns a copy. Read errors are
// logged and reported as "not found" so a hiccup never crashes a handler.
type Store struct {
	db *sql.DB
}

// Profile is what a player saved about themselves; the join form starts
// from it.
type Profile struct {
	UserID  string
	ClassID string
	RoleID  string
	GS      int
	// TimeZone is an IANA name such as "Europe/Prague"; empty means
	// guess from the player's Discord language.
	TimeZone string
}

const schema = `
CREATE TABLE IF NOT EXISTS guilds (
	guild_id     TEXT PRIMARY KEY,
	guild_name   TEXT NOT NULL,
	channel_id   TEXT NOT NULL,
	ping_role_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS posts (
	id          TEXT PRIMARY KEY,
	leader_id   TEXT NOT NULL,
	leader_name TEXT NOT NULL,
	guild_id    TEXT NOT NULL,
	guild_name  TEXT NOT NULL,
	dungeon_id  TEXT NOT NULL,
	mode_id     TEXT NOT NULL,
	min_gs      INTEGER NOT NULL,
	looking_for TEXT NOT NULL,
	missing     INTEGER NOT NULL,
	start       TEXT NOT NULL,
	note        TEXT NOT NULL,
	status      TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	closed_at   INTEGER NOT NULL,
	revision    INTEGER NOT NULL,
	pinged      TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS posts_by_leader ON posts (leader_id, status);
CREATE INDEX IF NOT EXISTS posts_by_status ON posts (status, expires_at);
CREATE TABLE IF NOT EXISTS players (
	post_id    TEXT NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	user_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	class_id   TEXT NOT NULL,
	role_id    TEXT NOT NULL,
	gs         INTEGER NOT NULL,
	guild_id   TEXT NOT NULL,
	guild_name TEXT NOT NULL,
	accepted   INTEGER NOT NULL,
	position   INTEGER NOT NULL,
	PRIMARY KEY (post_id, user_id)
);
CREATE TABLE IF NOT EXISTS messages (
	message_id   TEXT PRIMARY KEY,
	post_id      TEXT NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	guild_id     TEXT NOT NULL,
	channel_id   TEXT NOT NULL,
	ping_role_id TEXT NOT NULL,
	kind         TEXT NOT NULL DEFAULT 'card'
);
CREATE INDEX IF NOT EXISTS messages_by_post ON messages (post_id);
CREATE TABLE IF NOT EXISTS networks (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	code           TEXT NOT NULL UNIQUE,
	owner_guild_id TEXT NOT NULL,
	created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS network_members (
	guild_id   TEXT PRIMARY KEY,
	network_id TEXT NOT NULL REFERENCES networks (id) ON DELETE CASCADE,
	guild_name TEXT NOT NULL,
	joined_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS members_by_network ON network_members (network_id);
CREATE TABLE IF NOT EXISTS profiles (
	user_id    TEXT PRIMARY KEY,
	class_id   TEXT NOT NULL,
	role_id    TEXT NOT NULL,
	gs         INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);`

var postColumns = []string{
	"id", "leader_id", "leader_name", "guild_id", "guild_name", "dungeon_id", "mode_id", "min_gs", "looking_for",
	"missing", "start", "note", "status", "created_at", "expires_at", "closed_at", "revision", "pinged", "start_at",
}

var (
	selectPost = "SELECT " + strings.Join(postColumns, ", ") + " FROM posts"
	upsertPost = func() string {
		set := make([]string, 0, len(postColumns)-1)
		for _, c := range postColumns[1:] {
			set = append(set, c+" = excluded."+c)
		}
		return "INSERT INTO posts (" + strings.Join(postColumns, ", ") + ") VALUES (?" +
			strings.Repeat(", ?", len(postColumns)-1) + ") ON CONFLICT (id) DO UPDATE SET " + strings.Join(set, ", ")
	}()
)

const activeStatuses = "('open', 'full')"

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes all access, which is plenty for a bot
	// and makes every read-modify-write transaction atomic.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// migrations upgrade databases made by earlier versions, in order. The
// database's user_version counts how many have run; new databases get
// the base schema and then every migration.
var migrations = []string{
	`ALTER TABLE guilds ADD COLUMN lang TEXT NOT NULL DEFAULT 'en'`,
	`ALTER TABLE posts ADD COLUMN start_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE profiles ADD COLUMN timezone TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN thread INTEGER NOT NULL DEFAULT 0`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for ; version < len(migrations); version++ {
		if _, err := db.Exec(migrations[version]); err != nil {
			return fmt.Errorf("migration %d: %w", version+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version+1)); err != nil {
			return err
		}
	}
	return nil
}

func orEnglish(lang string) string {
	if lang == "" {
		return "en"
	}
	return lang
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetGuild(cfg GuildConfig) error {
	_, err := s.db.Exec(`INSERT INTO guilds (guild_id, guild_name, channel_id, ping_role_id, lang) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (guild_id) DO UPDATE SET guild_name = excluded.guild_name,
			channel_id = excluded.channel_id, ping_role_id = excluded.ping_role_id, lang = excluded.lang`,
		cfg.GuildID, cfg.GuildName, cfg.ChannelID, cfg.PingRoleID, orEnglish(cfg.Lang))
	return err
}

func (s *Store) RemoveGuild(guildID string) bool {
	res, err := s.db.Exec(`DELETE FROM guilds WHERE guild_id = ?`, guildID)
	if err != nil {
		log.Printf("store: remove guild %s: %v", guildID, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *Store) Guild(guildID string) (GuildConfig, bool) {
	g := GuildConfig{GuildID: guildID}
	err := s.db.QueryRow(`SELECT guild_name, channel_id, ping_role_id, lang FROM guilds WHERE guild_id = ?`, guildID).
		Scan(&g.GuildName, &g.ChannelID, &g.PingRoleID, &g.Lang)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("store: load guild %s: %v", guildID, err)
		}
		return GuildConfig{}, false
	}
	return g, true
}

func (s *Store) Guilds() []GuildConfig {
	rows, err := s.db.Query(`SELECT guild_id, guild_name, channel_id, ping_role_id, lang FROM guilds`)
	if err != nil {
		log.Printf("store: list guilds: %v", err)
		return nil
	}
	defer rows.Close()
	var out []GuildConfig
	for rows.Next() {
		var g GuildConfig
		if err := rows.Scan(&g.GuildID, &g.GuildName, &g.ChannelID, &g.PingRoleID, &g.Lang); err != nil {
			log.Printf("store: read guild: %v", err)
			return nil
		}
		out = append(out, g)
	}
	return out
}

func (s *Store) Post(id string) (Post, bool) {
	p, err := loadPost(s.db, id)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("store: load post %s: %v", id, err)
		}
		return Post{}, false
	}
	return *p, true
}

func (s *Store) ActivePostsByLeader(userID string) []Post {
	return s.postsWhere(`SELECT id FROM posts WHERE leader_id = ? AND status IN `+activeStatuses+` ORDER BY created_at, id`, userID)
}

func (s *Store) ActivePosts() []Post {
	return s.postsWhere(`SELECT id FROM posts WHERE status IN ` + activeStatuses + ` ORDER BY created_at, id`)
}

func (s *Store) postsWhere(query string, args ...any) []Post {
	ids, err := queryIDs(s.db, query, args...)
	if err != nil {
		log.Printf("store: list posts: %v", err)
		return nil
	}
	var out []Post
	for _, id := range ids {
		if p, ok := s.Post(id); ok {
			out = append(out, p)
		}
	}
	return out
}

// CreatePost assigns the post an ID and stores it, unless the leader
// already has maxActive open posts.
func (s *Store) CreatePost(p Post, maxActive int) (Post, error) {
	err := s.inTx(func(tx *sql.Tx) error {
		var busy int
		err := tx.QueryRow(`SELECT COUNT(*) FROM posts WHERE leader_id = ? AND status IN `+activeStatuses, p.LeaderID).Scan(&busy)
		if err != nil {
			return err
		}
		if busy >= maxActive {
			return ErrTooManyPosts
		}
		for {
			p.ID = newID()
			var taken int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM posts WHERE id = ?`, p.ID).Scan(&taken); err != nil {
				return err
			}
			if taken == 0 {
				break
			}
		}
		return writePost(tx, &p)
	})
	if err != nil {
		return Post{}, err
	}
	return p.Clone(), nil
}

// UpdatePost runs fn on the stored post inside a transaction and only
// commits if fn succeeds, so a failed check never leaves a half-applied
// change. The returned post is the stored one, changed or not. Changes fn
// makes to Messages are not saved; use AddMessages and RemoveMessage.
func (s *Store) UpdatePost(id string, fn func(p *Post) error) (Post, error) {
	var result Post
	err := s.inTx(func(tx *sql.Tx) error {
		cur, err := loadPost(tx, id)
		if err != nil {
			return err
		}
		result = cur.Clone()
		next := cur.Clone()
		if err := fn(&next); err != nil {
			return err
		}
		next.Revision++
		if err := writePost(tx, &next); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

// AddMessages records delivered copies of a post. It does not bump the
// revision because the post content did not change.
func (s *Store) AddMessages(id string, refs []MessageRef) (Post, bool) {
	var p *Post
	err := s.inTx(func(tx *sql.Tx) error {
		for _, m := range refs {
			_, err := tx.Exec(`INSERT OR REPLACE INTO messages (message_id, post_id, guild_id, channel_id, ping_role_id)
				VALUES (?, ?, ?, ?, ?)`, m.MessageID, id, m.GuildID, m.ChannelID, m.PingRoleID)
			if err != nil {
				return err
			}
		}
		var err error
		p, err = loadPost(tx, id)
		return err
	})
	if err != nil {
		log.Printf("store: save messages of post %s: %v", id, err)
		return Post{}, false
	}
	return *p, true
}

func (s *Store) RemoveMessage(id, messageID string) {
	if _, err := s.db.Exec(`DELETE FROM messages WHERE post_id = ? AND message_id = ?`, id, messageID); err != nil {
		log.Printf("store: forget message %s: %v", messageID, err)
	}
}

// ExpireDue marks active posts past their expiry and returns their IDs.
func (s *Store) ExpireDue(now time.Time) []string {
	var ids []string
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		ids, err = queryIDs(tx, `SELECT id FROM posts WHERE status IN `+activeStatuses+` AND expires_at < ?`, millis(now))
		if err != nil || len(ids) == 0 {
			return err
		}
		_, err = tx.Exec(`UPDATE posts SET status = ?, closed_at = ?, revision = revision + 1
			WHERE status IN `+activeStatuses+` AND expires_at < ?`, StatusExpired, millis(now), millis(now))
		return err
	})
	if err != nil {
		log.Printf("store: expire posts: %v", err)
		return nil
	}
	return ids
}

// SetThread records that the bot started a thread under a copy of a post.
func (s *Store) SetThread(id, messageID string) {
	_, err := s.db.Exec(`UPDATE messages SET thread = 1 WHERE post_id = ? AND message_id = ? AND kind = 'card'`, id, messageID)
	if err != nil {
		log.Printf("store: save thread of post %s: %v", id, err)
	}
}

// AddReply records a message the bot posted in a thread under a post, so
// it is deleted together with the post. Before threads, these were
// replies in the channel.
func (s *Store) AddReply(id string, m MessageRef) {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO messages (message_id, post_id, guild_id, channel_id, ping_role_id, kind)
		VALUES (?, ?, ?, ?, '', 'reply')`, m.MessageID, id, m.GuildID, m.ChannelID)
	if err != nil {
		log.Printf("store: save reply of post %s: %v", id, err)
	}
}

// EndedPost is a post due for deletion, with every message the bot posted
// for it: the copies of the card and the messages in their threads.
type EndedPost struct {
	ID       string
	Messages []MessageRef
}

// EndedBefore lists up to limit posts that ended before the cutoff.
func (s *Store) EndedBefore(cutoff time.Time, limit int) []EndedPost {
	ids, err := queryIDs(s.db, `SELECT id FROM posts WHERE status NOT IN `+activeStatuses+` AND closed_at < ?
		ORDER BY closed_at LIMIT ?`, millis(cutoff), limit)
	if err != nil {
		log.Printf("store: list ended posts: %v", err)
		return nil
	}
	out := make([]EndedPost, 0, len(ids))
	for _, id := range ids {
		out = append(out, EndedPost{ID: id, Messages: s.AllMessages(id)})
	}
	return out
}

// AllMessages lists every message the bot posted for a post: the copies of
// the card and the messages in their threads.
func (s *Store) AllMessages(id string) []MessageRef {
	rows, err := s.db.Query(`SELECT guild_id, channel_id, message_id, thread FROM messages WHERE post_id = ?`, id)
	if err != nil {
		log.Printf("store: list messages of post %s: %v", id, err)
		return nil
	}
	defer rows.Close()
	var out []MessageRef
	for rows.Next() {
		var m MessageRef
		if err := rows.Scan(&m.GuildID, &m.ChannelID, &m.MessageID, &m.Thread); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// DeletePost forgets a post, its players and its message records.
func (s *Store) DeletePost(id string) {
	if _, err := s.db.Exec(`DELETE FROM posts WHERE id = ?`, id); err != nil {
		log.Printf("store: delete post %s: %v", id, err)
	}
}

func (s *Store) Profile(userID string) (Profile, bool) {
	p := Profile{UserID: userID}
	err := s.db.QueryRow(`SELECT class_id, role_id, gs, timezone FROM profiles WHERE user_id = ?`, userID).
		Scan(&p.ClassID, &p.RoleID, &p.GS, &p.TimeZone)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("store: load profile %s: %v", userID, err)
		}
		return Profile{}, false
	}
	return p, true
}

func (s *Store) SetProfile(p Profile) error {
	_, err := s.db.Exec(`INSERT INTO profiles (user_id, class_id, role_id, gs, timezone, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET class_id = excluded.class_id, role_id = excluded.role_id,
			gs = excluded.gs, timezone = excluded.timezone, updated_at = excluded.updated_at`,
		p.UserID, p.ClassID, p.RoleID, p.GS, p.TimeZone, millis(time.Now()))
	return err
}

// ImportJSON copies server settings and posts from the JSON file used by
// earlier versions, then renames the file so it is imported only once.
func (s *Store) ImportJSON(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var old struct {
		Guilds map[string]*GuildConfig `json:"guilds"`
		Posts  map[string]*Post        `json:"posts"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	err = s.inTx(func(tx *sql.Tx) error {
		ids := slices.Sorted(maps.Keys(old.Guilds))
		for _, id := range ids {
			g := old.Guilds[id]
			_, err := tx.Exec(`INSERT OR REPLACE INTO guilds (guild_id, guild_name, channel_id, ping_role_id, lang) VALUES (?, ?, ?, ?, ?)`,
				g.GuildID, g.GuildName, g.ChannelID, g.PingRoleID, orEnglish(g.Lang))
			if err != nil {
				return err
			}
		}
		// Earlier versions shared posts between every server, so the
		// imported servers stay linked to each other.
		if len(ids) > 1 {
			if err := importNetwork(tx, old.Guilds, ids); err != nil {
				return err
			}
		}
		for _, p := range old.Posts {
			if err := writePost(tx, p); err != nil {
				return err
			}
			for _, m := range p.Messages {
				_, err := tx.Exec(`INSERT OR REPLACE INTO messages (message_id, post_id, guild_id, channel_id, ping_role_id)
					VALUES (?, ?, ?, ?, ?)`, m.MessageID, p.ID, m.GuildID, m.ChannelID, m.PingRoleID)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("import %s: %w", path, err)
	}
	return true, os.Rename(path, path+".imported")
}

func importNetwork(tx *sql.Tx, guilds map[string]*GuildConfig, ids []string) error {
	code, err := freeCode(tx)
	if err != nil {
		return err
	}
	id := newID()
	_, err = tx.Exec(`INSERT INTO networks (id, name, code, owner_guild_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, "Linked servers", code, ids[0], millis(time.Now()))
	if err != nil {
		return err
	}
	for _, g := range ids {
		if err := addMember(tx, id, g, guilds[g].GuildName); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) inTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func loadPost(q querier, id string) (*Post, error) {
	var p Post
	var lookingFor, pinged string
	var created, expires, closed, startAt int64
	err := q.QueryRow(selectPost+" WHERE id = ?", id).Scan(
		&p.ID, &p.LeaderID, &p.LeaderName, &p.GuildID, &p.GuildName, &p.DungeonID, &p.ModeID, &p.MinGS, &lookingFor,
		&p.Missing, &p.Start, &p.Note, &p.Status, &created, &expires, &closed, &p.Revision, &pinged, &startAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(lookingFor), &p.LookingFor); err != nil {
		return nil, fmt.Errorf("post %s looking_for: %w", id, err)
	}
	if err := json.Unmarshal([]byte(pinged), &p.Pinged); err != nil {
		return nil, fmt.Errorf("post %s pinged: %w", id, err)
	}
	p.CreatedAt, p.ExpiresAt, p.ClosedAt = fromMillis(created), fromMillis(expires), fromMillis(closed)
	p.StartAt = fromMillis(startAt)

	rows, err := q.Query(`SELECT user_id, name, class_id, role_id, gs, guild_id, guild_name, accepted FROM players
		WHERE post_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pl Player
		var accepted bool
		if err := rows.Scan(&pl.UserID, &pl.Name, &pl.ClassID, &pl.RoleID, &pl.GS, &pl.GuildID, &pl.GuildName, &accepted); err != nil {
			return nil, err
		}
		if accepted {
			p.Accepted = append(p.Accepted, pl)
		} else {
			p.Applicants = append(p.Applicants, pl)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	msgs, err := q.Query(`SELECT guild_id, channel_id, message_id, ping_role_id, thread FROM messages
		WHERE post_id = ? AND kind = 'card' ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer msgs.Close()
	for msgs.Next() {
		var m MessageRef
		if err := msgs.Scan(&m.GuildID, &m.ChannelID, &m.MessageID, &m.PingRoleID, &m.Thread); err != nil {
			return nil, err
		}
		p.Messages = append(p.Messages, m)
	}
	return &p, msgs.Err()
}

// writePost saves the post row and its players; message refs are kept as
// they are.
func writePost(tx *sql.Tx, p *Post) error {
	lookingFor, err := json.Marshal(p.LookingFor)
	if err != nil {
		return err
	}
	pinged, err := json.Marshal(append([]string{}, p.Pinged...))
	if err != nil {
		return err
	}
	_, err = tx.Exec(upsertPost,
		p.ID, p.LeaderID, p.LeaderName, p.GuildID, p.GuildName, p.DungeonID, p.ModeID, p.MinGS, string(lookingFor),
		p.Missing, p.Start, p.Note, p.Status, millis(p.CreatedAt), millis(p.ExpiresAt), millis(p.ClosedAt), p.Revision,
		string(pinged), millis(p.StartAt))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM players WHERE post_id = ?`, p.ID); err != nil {
		return err
	}
	position := 0
	for _, group := range []struct {
		players  []Player
		accepted bool
	}{{p.Accepted, true}, {p.Applicants, false}} {
		for _, pl := range group.players {
			position++
			_, err := tx.Exec(`INSERT INTO players (post_id, user_id, name, class_id, role_id, gs, guild_id, guild_name, accepted, position)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				p.ID, pl.UserID, pl.Name, pl.ClassID, pl.RoleID, pl.GS, pl.GuildID, pl.GuildName, group.accepted, position)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func queryIDs(q querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func newID() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
