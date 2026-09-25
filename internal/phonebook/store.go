// Package phonebook manages personal contacts, scoped to their owner.
package phonebook

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dmalkin/westbridge/internal/telephony"
)

// Store borrows the application database; it does not own its lifetime.
type Store struct{ db *sql.DB }

// New uses an initialized database whose lifetime is owned by the caller.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Contact belongs to one user; ownership is never accepted from request bodies.
type Contact struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number"`
}

// Contact validation and ownership errors safe to report to clients.
var (
	ErrContactInvalid   = errors.New("invalid contact")
	ErrContactMissing   = errors.New("contact not found")
	ErrContactDuplicate = errors.New("this number is already in your phonebook")
)

// Contacts returns only the owner's entries.
func (s *Store) Contacts(ctx context.Context, owner int64) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,number FROM contacts WHERE user_id=? ORDER BY name COLLATE NOCASE,id", owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	contacts := []Contact{}
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Number); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// SaveContact creates (id=0) or replaces an entry within the owner's book.
func (s *Store) SaveContact(ctx context.Context, owner, id int64, name, number string) (Contact, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 || strings.ContainsFunc(name, unicode.IsControl) {
		return Contact{}, fmt.Errorf("%w: name must contain 1–100 characters without control characters", ErrContactInvalid)
	}
	number, err := telephony.NormalizeNumber(number)
	if err != nil {
		return Contact{}, fmt.Errorf("%w: %s", ErrContactInvalid, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Contact{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if id != 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM contacts WHERE user_id=? AND id=?)", owner, id).Scan(&exists); err != nil {
			return Contact{}, err
		}
		if !exists {
			return Contact{}, ErrContactMissing
		}
	}
	var duplicate bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM contacts WHERE user_id=? AND number=? AND id<>?)", owner, number, id).Scan(&duplicate); err != nil {
		return Contact{}, err
	}
	if duplicate {
		return Contact{}, ErrContactDuplicate
	}
	if id == 0 {
		result, err := tx.ExecContext(ctx, "INSERT INTO contacts(user_id,name,number) VALUES(?,?,?)", owner, name, number)
		if err != nil {
			return Contact{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return Contact{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE contacts SET name=?,number=? WHERE user_id=? AND id=?", name, number, owner, id); err != nil {
			return Contact{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Contact{}, err
	}
	return Contact{ID: id, Name: name, Number: number}, nil
}

// DeleteContact cannot delete another user's entry, even for administrators.
func (s *Store) DeleteContact(ctx context.Context, owner, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM contacts WHERE user_id=? AND id=?", owner, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrContactMissing
	}
	return nil
}
