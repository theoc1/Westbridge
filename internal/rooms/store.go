// Package rooms owns room definitions, provisioning intent and user grants.
package rooms

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Catalogue errors safe for HTTP clients.
var (
	ErrMissing  = errors.New("room not found")
	ErrInvalid  = errors.New("invalid room or user assignment")
	ErrConflict = errors.New("room number is already reserved")
	ErrBusy     = errors.New("room has participants or outgoing calls")
	ErrNotReady = errors.New("room is not ready for new calls")
)

// Room contains persistent definition and the last provisioning result.
type Room struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	Name       string `json:"name"`
	Bridge     string `json:"-"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Generation int64  `json:"-"`
}

// Store borrows the shared application database. The configured range must be
// reserved for rooms in the deployment's dialplan.
type Store struct {
	db       *sql.DB
	min, max int
}

// New constructs a catalogue for an explicitly reserved numeric range.
func New(db *sql.DB, minimum, maximum int) *Store { return &Store{db: db, min: minimum, max: maximum} }

var digits = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// ImportLegacy imports the old configured room exactly once on upgraded files.
func (s *Store) ImportLegacy(number string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var pending string
	if err = tx.QueryRow("SELECT value FROM app_metadata WHERE key='legacy_room_pending'").Scan(&pending); err != nil {
		return err
	}
	if pending == "yes" && number != "" {
		if !digits.MatchString(number) {
			return ErrInvalid
		}
		_, err = tx.Exec("INSERT INTO rooms(id,number,name,bridge) VALUES('legacy',?,?,?)", number, "Conference "+number, number)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO room_users(room_id,user_id) SELECT 'legacy',id FROM users"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE app_metadata SET value='done' WHERE key='legacy_room_pending'"); err != nil {
		return err
	}
	return tx.Commit()
}

// Create records pending provisioning; no network effects occur in the transaction.
func (s *Store) Create(ctx context.Context, name, number string) (Room, error) {
	name = strings.TrimSpace(name)
	var n int
	if !digits.MatchString(number) {
		return Room{}, ErrInvalid
	}
	if _, err := fmt.Sscan(number, &n); err != nil || n < s.min || n > s.max {
		return Room{}, fmt.Errorf("%w: number must be %d–%d", ErrInvalid, s.min, s.max)
	}
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 || strings.ContainsFunc(name, unicode.IsControl) {
		return Room{}, ErrInvalid
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Room{}, err
	}
	r := Room{ID: hex.EncodeToString(token[:]), Number: number, Name: name, State: "pending", Generation: 1}
	r.Bridge = "wb-" + r.ID
	result, err := s.db.ExecContext(ctx, "INSERT INTO rooms(id,number,name,bridge) VALUES(?,?,?,?) ON CONFLICT(number) WHERE state<>'deleted' DO NOTHING", r.ID, r.Number, r.Name, r.Bridge)
	if err != nil {
		return Room{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Room{}, err
	}
	if count == 0 {
		return Room{}, ErrConflict
	}
	return r, nil
}

const columns = "id,number,name,bridge,state,error,generation"

// All includes tombstones, whose bridge identities reject delayed events safely.
func (s *Store) All(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM rooms ORDER BY number")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Room{}
	for rows.Next() {
		var r Room
		if err = rows.Scan(&r.ID, &r.Number, &r.Name, &r.Bridge, &r.State, &r.Error, &r.Generation); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get resolves even a tombstone for reconciliation.
func (s *Store) Get(ctx context.Context, id string) (Room, error) {
	var r Room
	err := s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM rooms WHERE id=?", id).Scan(&r.ID, &r.Number, &r.Name, &r.Bridge, &r.State, &r.Error, &r.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrMissing
	}
	return r, err
}

// Allowed checks current role from the database, so demotion takes effect on sockets.
func (s *Store) Allowed(ctx context.Context, id string, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN rooms r ON r.id=? WHERE u.id=? AND u.enabled=1 AND r.state<>'deleted' AND (u.role='admin' OR (r.state<>'deleting' AND EXISTS(SELECT 1 FROM room_users ru WHERE ru.user_id=u.id AND ru.room_id=r.id))))`, id, user).Scan(&ok)
	return ok, err
}

// Grants returns assigned user IDs (administrators have implicit access).
func (s *Store) Grants(ctx context.Context, id string) ([]int64, error) {
	r, err := s.Get(ctx, id)
	if err != nil || r.State == "deleted" {
		return nil, ErrMissing
	}
	rows, err := s.db.QueryContext(ctx, "SELECT user_id FROM room_users WHERE room_id=? ORDER BY user_id", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var user int64
		if err = rows.Scan(&user); err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	return out, rows.Err()
}

// SetGrants atomically replaces assignments, rejecting invalid identities.
func (s *Store) SetGrants(ctx context.Context, id string, users []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM rooms WHERE id=?", id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return ErrMissing
	} else if err != nil {
		return err
	}
	if state == "deleted" || state == "deleting" {
		return ErrMissing
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM room_users WHERE room_id=?", id); err != nil {
		return err
	}
	for _, user := range users {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=?)", user).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO room_users(room_id,user_id) VALUES(?,?)", id, user); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Deleting persists deletion before touching Asterisk.
func (s *Store) Deleting(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE rooms SET state='deleting',generation=generation+1,error='' WHERE id=? AND state NOT IN ('deleting','deleted')", id)
	return err
}

// Result accepts only a result for the generation that produced it.
func (s *Store) Result(ctx context.Context, r Room, state string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
		state = r.State
		if state == "ready" {
			state = "pending"
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "UPDATE rooms SET state=?,error=? WHERE id=? AND generation=?", state, message, r.ID, r.Generation)
	if err != nil {
		return err
	}
	if state == "deleted" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM room_users WHERE room_id=? AND EXISTS(SELECT 1 FROM rooms WHERE id=? AND state='deleted')", r.ID, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
