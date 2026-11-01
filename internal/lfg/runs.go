package lfg

import (
	"database/sql"
	"log"
	"strings"
	"time"
)

// Run is what the stats keep of a post. Posts are deleted a day after
// they end; their runs stay until PruneRuns drops them.
type Run struct {
	GuildID   string
	DungeonID string
	ModeID    string
	Status    Status
	CreatedAt time.Time
	// FilledAt is when the party was first full; zero if it never was.
	FilledAt time.Time
	// Players is how many players were on the post when it ended, or are
	// on it now.
	Players int
	// Unlimited marks a rally without a size, which never fills.
	Unlimited bool
}

func runOf(p *Post) Run {
	return Run{
		GuildID: p.GuildID, DungeonID: p.DungeonID, ModeID: p.ModeID, Status: p.Status,
		CreatedAt: p.CreatedAt, FilledAt: p.FilledAt, Players: len(p.Accepted) + len(p.Applicants) + len(p.Reserves),
		Unlimited: p.Rally && p.Size == 0,
	}
}

// recordRun keeps an ended post for the stats.
func recordRun(tx *sql.Tx, p *Post) error {
	r := runOf(p)
	_, err := tx.Exec(`INSERT OR REPLACE INTO runs (post_id, guild_id, dungeon_id, mode_id, status, created_at, filled_at, players,
		unlimited) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, r.GuildID, r.DungeonID, r.ModeID, r.Status, millis(r.CreatedAt), millis(r.FilledAt), r.Players, r.Unlimited)
	return err
}

// Runs lists the posts made on the given servers since a time: the ended
// ones from the stats, and the open ones as they are now.
func (s *Store) Runs(guildIDs []string, since time.Time) []Run {
	if len(guildIDs) == 0 {
		return nil
	}
	in := "(?" + strings.Repeat(", ?", len(guildIDs)-1) + ")"
	args := make([]any, 0, len(guildIDs)+1)
	for _, id := range guildIDs {
		args = append(args, id)
	}
	args = append(args, millis(since))

	out, err := s.endedRuns(`SELECT guild_id, dungeon_id, mode_id, status, created_at, filled_at, players, unlimited FROM runs
		WHERE guild_id IN `+in+` AND created_at >= ?`, args...)
	if err != nil {
		log.Printf("store: list runs: %v", err)
		return nil
	}
	for _, p := range s.postsWhere(`SELECT id FROM posts WHERE status IN `+activeStatuses+` AND guild_id IN `+in+
		` AND created_at >= ?`, args...) {
		out = append(out, runOf(&p))
	}
	return out
}

// endedRuns reads runs and closes the rows before returning, as the store
// has a single connection.
func (s *Store) endedRuns(query string, args ...any) ([]Run, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var created, filled int64
		if err := rows.Scan(&r.GuildID, &r.DungeonID, &r.ModeID, &r.Status, &created, &filled, &r.Players, &r.Unlimited); err != nil {
			return nil, err
		}
		r.CreatedAt, r.FilledAt = fromMillis(created), fromMillis(filled)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneRuns forgets runs posted before the cutoff.
func (s *Store) PruneRuns(cutoff time.Time) {
	if _, err := s.db.Exec(`DELETE FROM runs WHERE created_at < ?`, millis(cutoff)); err != nil {
		log.Printf("store: prune runs: %v", err)
	}
}
