package database_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/database"
	"github.com/dmalkin/westbridge/internal/phonebook"
)

// Build the pre-split schema directly, so opening an existing application file
// is tested independently of the new initializer that also creates fresh files.
func TestOpenExistingDatabasePreservesAccountsSessionsAndContacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	_, err = old.Exec(`
 CREATE TABLE users (id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('admin','user')), enabled INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL);
 CREATE TABLE contacts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, number TEXT NOT NULL, UNIQUE(user_id, number));`)
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.New(old)
	user, err := accounts.Bootstrap("admin", "abc")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := accounts.Login("admin", "abc")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := phonebook.New(old).SaveContact(context.Background(), user.ID, 0, "Friend", "1002")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	restored, err := auth.New(db).Authenticate(token)
	if err != nil || restored != user {
		t.Fatalf("session lost: %+v %v", restored, err)
	}
	if _, _, err := auth.New(db).Login("admin", "abc"); err != nil {
		t.Fatal(err)
	}
	contacts, err := phonebook.New(db).Contacts(context.Background(), user.ID)
	if err != nil || len(contacts) != 1 || contacts[0] != contact {
		t.Fatalf("contacts lost: %+v %v", contacts, err)
	}
	if _, err := phonebook.New(db).SaveContact(context.Background(), user.ID+100, 0, "No owner", "1003"); err == nil {
		t.Fatal("foreign key enforcement disabled")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v", info.Mode())
	}
}

func TestFailedRoomMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A conflicting table forces failure after the migration has begun.
	if _, err = db.Exec(`CREATE TABLE rooms (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if opened, err := database.Open(path); err == nil {
		_ = opened.Close()
		t.Fatal("accepted conflicting schema")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version, tables int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('room_users','app_metadata','rooms_active_number')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 0 || tables != 0 {
		t.Fatalf("partial migration: version=%d objects=%d", version, tables)
	}
}
