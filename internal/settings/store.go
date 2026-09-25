// Package settings stores installation-wide application preferences.
package settings

import (
	"context"
	"database/sql"
	"errors"
)

// ErrInvalidLocale rejects languages without a shipped translation.
var ErrInvalidLocale = errors.New("unsupported language")

// Store borrows the shared application database.
type Store struct{ db *sql.DB }

// New constructs a store without changing persisted settings.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Locale returns the installation language, defaulting to English.
func (s *Store) Locale(ctx context.Context) (string, error) {
	var locale string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM app_metadata WHERE key='locale'").Scan(&locale)
	if errors.Is(err, sql.ErrNoRows) {
		return "en", nil
	}
	return locale, err
}

// SetLocale persists one of the supported installation languages.
func (s *Store) SetLocale(ctx context.Context, locale string) error {
	if locale != "en" && locale != "ru" {
		return ErrInvalidLocale
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO app_metadata(key,value) VALUES('locale',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", locale)
	return err
}
