package database

import (
	"database/sql"
	"fmt"
)

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than this application", version)
	}
	if version == 0 {
		_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS rooms (
 id TEXT PRIMARY KEY, number TEXT NOT NULL, name TEXT NOT NULL, bridge TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ready','deleting','deleted')),
 error TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 1);
CREATE UNIQUE INDEX rooms_active_number ON rooms(number) WHERE state<>'deleted';
CREATE TABLE IF NOT EXISTS room_users (
 room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, PRIMARY KEY(room_id,user_id));
CREATE TABLE IF NOT EXISTS app_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO app_metadata(key,value) SELECT 'legacy_room_pending', CASE WHEN EXISTS(SELECT 1 FROM users) THEN 'yes' ELSE 'no' END;
PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
