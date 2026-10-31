package lfg

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

// Alert is what a player wants a DM about: new posts for one of their
// dungeons that they could join with one of their class and role choices.
type Alert struct {
	UserID string
	// GuildID is the server the player set the alert up on; posts that
	// reach it count, and the DM links to the copy there.
	GuildID string
	// Lang is the language the DMs are written in.
	Lang     string
	Dungeons []string
	// Choices are "class|role" pairs, as in the join form.
	Choices []string
}

// On reports whether the alert can match anything.
func (a Alert) On() bool { return len(a.Dungeons) > 0 && len(a.Choices) > 0 }

func (s *Store) Alert(userID string) (Alert, bool) {
	rows, err := s.alerts(`SELECT user_id, guild_id, lang, dungeons, choices FROM alerts WHERE user_id = ?`, userID)
	if err != nil {
		log.Printf("store: load alert of %s: %v", userID, err)
	}
	if len(rows) == 0 {
		return Alert{}, false
	}
	return rows[0], true
}

func (s *Store) SetAlert(a Alert) error {
	dungeons, err := json.Marshal(append([]string{}, a.Dungeons...))
	if err != nil {
		return err
	}
	choices, err := json.Marshal(append([]string{}, a.Choices...))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO alerts (user_id, guild_id, lang, dungeons, choices, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET guild_id = excluded.guild_id, lang = excluded.lang,
			dungeons = excluded.dungeons, choices = excluded.choices, updated_at = excluded.updated_at`,
		a.UserID, a.GuildID, orEnglish(a.Lang), string(dungeons), string(choices), millis(time.Now()))
	return err
}

func (s *Store) RemoveAlert(userID string) {
	if _, err := s.db.Exec(`DELETE FROM alerts WHERE user_id = ?`, userID); err != nil {
		log.Printf("store: remove alert of %s: %v", userID, err)
	}
}

// AlertsIn lists the alerts set up on the given servers.
func (s *Store) AlertsIn(guildIDs []string) []Alert {
	if len(guildIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(guildIDs))
	for _, id := range guildIDs {
		args = append(args, id)
	}
	out, err := s.alerts(`SELECT user_id, guild_id, lang, dungeons, choices FROM alerts
		WHERE guild_id IN (?`+strings.Repeat(", ?", len(guildIDs)-1)+`) ORDER BY user_id`, args...)
	if err != nil {
		log.Printf("store: list alerts: %v", err)
	}
	return out
}

func (s *Store) alerts(query string, args ...any) ([]Alert, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var dungeons, choices string
		if err := rows.Scan(&a.UserID, &a.GuildID, &a.Lang, &dungeons, &choices); err != nil {
			return out, err
		}
		if err := errors.Join(json.Unmarshal([]byte(dungeons), &a.Dungeons), json.Unmarshal([]byte(choices), &a.Choices)); err != nil {
			return out, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
