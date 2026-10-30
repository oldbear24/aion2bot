package lfg

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

// A Network is a group of linked servers. Posts are only shared between
// servers of the same network; a server that isn't linked only sees its
// own posts.
type Network struct {
	ID           string
	Name         string
	Code         string
	OwnerGuildID string
	Members      []NetworkMember
}

type NetworkMember struct {
	GuildID   string
	GuildName string
}

var (
	ErrInNetwork   = errors.New("server is already in a network")
	ErrNoNetwork   = errors.New("server is not in a network")
	ErrBadCode     = errors.New("no network has this code")
	ErrNotOwner    = errors.New("only the server that created the network can do this")
	ErrNotMember   = errors.New("server is not in this network")
	ErrRemoveOwner = errors.New("the owner server can't remove itself")
)

func (s *Store) CreateNetwork(name, guildID, guildName string) (Network, error) {
	n := Network{ID: newID(), Name: name, OwnerGuildID: guildID}
	err := s.inTx(func(tx *sql.Tx) error {
		if _, ok, err := networkIDOf(tx, guildID); err != nil {
			return err
		} else if ok {
			return ErrInNetwork
		}
		code, err := freeCode(tx)
		if err != nil {
			return err
		}
		n.Code = code
		_, err = tx.Exec(`INSERT INTO networks (id, name, code, owner_guild_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			n.ID, n.Name, n.Code, guildID, millis(time.Now()))
		if err != nil {
			return err
		}
		return addMember(tx, n.ID, guildID, guildName)
	})
	if err != nil {
		return Network{}, err
	}
	return s.networkByID(n.ID)
}

func (s *Store) JoinNetwork(code, guildID, guildName string) (Network, error) {
	var id string
	err := s.inTx(func(tx *sql.Tx) error {
		if _, ok, err := networkIDOf(tx, guildID); err != nil {
			return err
		} else if ok {
			return ErrInNetwork
		}
		err := tx.QueryRow(`SELECT id FROM networks WHERE code = ?`, code).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBadCode
		}
		if err != nil {
			return err
		}
		return addMember(tx, id, guildID, guildName)
	})
	if err != nil {
		return Network{}, err
	}
	return s.networkByID(id)
}

// LeaveNetwork takes a server out of its network. If the owner leaves, the
// longest-standing member takes over; an empty network is deleted.
func (s *Store) LeaveNetwork(guildID string) (Network, error) {
	var left Network
	err := s.inTx(func(tx *sql.Tx) error {
		id, ok, err := networkIDOf(tx, guildID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoNetwork
		}
		if left, err = loadNetwork(tx, id); err != nil {
			return err
		}
		return removeMember(tx, left, guildID)
	})
	return left, err
}

// RemoveFromNetwork lets the owner server drop another server.
func (s *Store) RemoveFromNetwork(ownerGuildID, guildID string) (Network, error) {
	var n Network
	err := s.inTx(func(tx *sql.Tx) error {
		id, ok, err := networkIDOf(tx, ownerGuildID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoNetwork
		}
		if n, err = loadNetwork(tx, id); err != nil {
			return err
		}
		switch {
		case n.OwnerGuildID != ownerGuildID:
			return ErrNotOwner
		case guildID == ownerGuildID:
			return ErrRemoveOwner
		}
		for _, m := range n.Members {
			if m.GuildID == guildID {
				return removeMember(tx, n, guildID)
			}
		}
		return ErrNotMember
	})
	if err != nil {
		return Network{}, err
	}
	return s.networkByID(n.ID)
}

// NewNetworkCode replaces the join code, so the old one stops working.
func (s *Store) NewNetworkCode(ownerGuildID string) (Network, error) {
	var id string
	err := s.inTx(func(tx *sql.Tx) error {
		var ok bool
		var err error
		if id, ok, err = networkIDOf(tx, ownerGuildID); err != nil {
			return err
		} else if !ok {
			return ErrNoNetwork
		}
		var owner string
		if err := tx.QueryRow(`SELECT owner_guild_id FROM networks WHERE id = ?`, id).Scan(&owner); err != nil {
			return err
		}
		if owner != ownerGuildID {
			return ErrNotOwner
		}
		code, err := freeCode(tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE networks SET code = ? WHERE id = ?`, code, id)
		return err
	})
	if err != nil {
		return Network{}, err
	}
	return s.networkByID(id)
}

// NetworkOf returns the network a server belongs to.
func (s *Store) NetworkOf(guildID string) (Network, bool) {
	id, ok, err := networkIDOf(s.db, guildID)
	if err != nil || !ok {
		return Network{}, false
	}
	n, err := s.networkByID(id)
	return n, err == nil
}

// Targets lists the LFG channels a post from this server goes to: its own
// and those of every server in its network.
func (s *Store) Targets(guildID string) []GuildConfig {
	rows, err := s.db.Query(`SELECT guild_id, guild_name, channel_id, ping_role_id, lang FROM guilds
		WHERE guild_id = ? OR guild_id IN (
			SELECT guild_id FROM network_members WHERE network_id = (
				SELECT network_id FROM network_members WHERE guild_id = ?))`, guildID, guildID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []GuildConfig
	for rows.Next() {
		var g GuildConfig
		if err := rows.Scan(&g.GuildID, &g.GuildName, &g.ChannelID, &g.PingRoleID, &g.Lang); err != nil {
			return nil
		}
		out = append(out, g)
	}
	return out
}

// Linked lists the servers whose posts a server sees: itself and its
// network.
func (s *Store) Linked(guildID string) []string {
	ids, err := queryIDs(s.db, `SELECT guild_id FROM network_members WHERE network_id = (
		SELECT network_id FROM network_members WHERE guild_id = ?)`, guildID)
	if err != nil || len(ids) == 0 {
		return []string{guildID}
	}
	return ids
}

func (s *Store) networkByID(id string) (Network, error) { return loadNetwork(s.db, id) }

func loadNetwork(q querier, id string) (Network, error) {
	n := Network{ID: id}
	err := q.QueryRow(`SELECT name, code, owner_guild_id FROM networks WHERE id = ?`, id).Scan(&n.Name, &n.Code, &n.OwnerGuildID)
	if err != nil {
		return Network{}, err
	}
	rows, err := q.Query(`SELECT guild_id, guild_name FROM network_members WHERE network_id = ? ORDER BY joined_at, guild_id`, id)
	if err != nil {
		return Network{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var m NetworkMember
		if err := rows.Scan(&m.GuildID, &m.GuildName); err != nil {
			return Network{}, err
		}
		n.Members = append(n.Members, m)
	}
	return n, rows.Err()
}

func networkIDOf(q querier, guildID string) (string, bool, error) {
	var id string
	err := q.QueryRow(`SELECT network_id FROM network_members WHERE guild_id = ?`, guildID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func addMember(tx *sql.Tx, networkID, guildID, guildName string) error {
	_, err := tx.Exec(`INSERT INTO network_members (guild_id, network_id, guild_name, joined_at) VALUES (?, ?, ?, ?)`,
		guildID, networkID, guildName, millis(time.Now()))
	return err
}

func removeMember(tx *sql.Tx, n Network, guildID string) error {
	if _, err := tx.Exec(`DELETE FROM network_members WHERE guild_id = ?`, guildID); err != nil {
		return err
	}
	var next string
	for _, m := range n.Members {
		if m.GuildID != guildID {
			next = m.GuildID
			break
		}
	}
	if next == "" {
		_, err := tx.Exec(`DELETE FROM networks WHERE id = ?`, n.ID)
		return err
	}
	if n.OwnerGuildID == guildID {
		_, err := tx.Exec(`UPDATE networks SET owner_guild_id = ? WHERE id = ?`, next, n.ID)
		return err
	}
	return nil
}

// freeCode returns an unused join code like "K7M2QX9P", without letters
// and digits that are easy to mix up.
func freeCode(tx *sql.Tx) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for {
		b := make([]byte, 8)
		rand.Read(b)
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM networks WHERE code = ?`, string(b)).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return string(b), nil
		}
	}
}
