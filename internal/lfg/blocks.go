package lfg

import (
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"
)

// A Block keeps a player away from one Discord server, or from a whole
// network: their posts don't arrive there, and they can't post or join
// runs from there.
type Block struct {
	// Scope is a server ID, or NetworkScope of a network.
	Scope  string
	UserID string
	// Name is the player's name when they were blocked, to list them by.
	Name string
	// By is the moderator who blocked them.
	By string
	At time.Time
}

const networkPrefix = "net:"

// NetworkScope is the scope of a block across a whole network.
func NetworkScope(networkID string) string { return networkPrefix + networkID }

// Network reports whether the block covers a whole network.
func (b Block) Network() bool { return strings.HasPrefix(b.Scope, networkPrefix) }

func (s *Store) Block(b Block) error {
	if b.At.IsZero() {
		b.At = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO blocks (scope, user_id, name, blocked_by, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (scope, user_id) DO UPDATE SET name = excluded.name, blocked_by = excluded.blocked_by`,
		b.Scope, b.UserID, b.Name, b.By, millis(b.At))
	return err
}

// Unblock lifts a block and reports whether there was one.
func (s *Store) Unblock(scope, userID string) bool {
	res, err := s.db.Exec(`DELETE FROM blocks WHERE scope = ? AND user_id = ?`, scope, userID)
	if err != nil {
		log.Printf("store: unblock %s in %s: %v", userID, scope, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// Blocks lists the blocks in the given scopes, the newest first.
func (s *Store) Blocks(scopes ...string) []Block {
	if len(scopes) == 0 {
		return nil
	}
	args := make([]any, 0, len(scopes))
	for _, sc := range scopes {
		args = append(args, sc)
	}
	rows, err := s.db.Query(`SELECT scope, user_id, name, blocked_by, created_at FROM blocks
		WHERE scope IN (?`+strings.Repeat(", ?", len(scopes)-1)+`) ORDER BY created_at DESC, user_id`, args...)
	if err != nil {
		log.Printf("store: list blocks: %v", err)
		return nil
	}
	defer rows.Close()
	var out []Block
	for rows.Next() {
		var b Block
		var at int64
		if err := rows.Scan(&b.Scope, &b.UserID, &b.Name, &b.By, &at); err != nil {
			log.Printf("store: read block: %v", err)
			return out
		}
		b.At = fromMillis(at)
		out = append(out, b)
	}
	return out
}

// Blocked reports whether a player is blocked on a Discord server, or in
// the network it belongs to.
func (s *Store) Blocked(userID, guildID string) bool {
	if userID == "" || guildID == "" {
		return false
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM blocks WHERE user_id = ? AND (scope = ? OR scope = ? || (
		SELECT network_id FROM network_members WHERE guild_id = ?)) LIMIT 1`, userID, guildID, networkPrefix, guildID).Scan(&one)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("store: check block of %s in %s: %v", userID, guildID, err)
		}
		return false
	}
	return true
}
