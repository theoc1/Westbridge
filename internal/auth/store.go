// Package auth persists users, revocable sessions and personal contacts in SQLite.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite" // Register the database/sql driver.
)

// Authentication and validation errors safe to report to the client.
var (
	ErrCredentials = errors.New("invalid login or password")
	ErrSession     = errors.New("session expired; please sign in")
	ErrInvalid     = errors.New("invalid user")
	ErrConflict    = errors.New("login already exists")
	ErrNotFound    = errors.New("user not found")
	ErrLastAdmin   = errors.New("cannot disable or demote the last active administrator")
)

// SessionTTL is the absolute lifetime of a login session.
const SessionTTL = 12 * time.Hour

// User is the public identity; it never contains password or session secrets.
type User struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Role    string `json:"role"`
	Enabled bool   `json:"enabled"`
}

// Store owns the users, sessions and contacts database.
type Store struct{ db *sql.DB }

// Open initializes or opens the persistent database; :memory: is useful in tests.
func Open(filename string) (*Store, error) {
	if filename != ":memory:" {
		if filename == "" {
			return nil, errors.New("database path is required")
		}
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
		if err := os.Chmod(filename, 0600); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
 PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS users (
 id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('admin','user')), enabled INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
 CREATE TABLE IF NOT EXISTS contacts (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, number TEXT NOT NULL, UNIQUE(user_id, number));`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases database resources.
func (s *Store) Close() error { return s.db.Close() }

var validLogin = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// NormalizeLogin makes login matching case insensitive.
func NormalizeLogin(login string) string { return strings.ToLower(strings.TrimSpace(login)) }
func validate(login, role, password string) error {
	if !validLogin.MatchString(login) {
		return fmt.Errorf("%w: login must be 1–64 letters, digits, dots, underscores or hyphens", ErrInvalid)
	}
	if role != "admin" && role != "user" {
		return fmt.Errorf("%w: role must be admin or user", ErrInvalid)
	}
	if utf8.RuneCountInString(password) < 3 || len(password) > 1024 {
		return fmt.Errorf("%w: password must be at least 3 characters and at most 1024 bytes", ErrInvalid)
	}
	return nil
}

// Parameters follow OWASP's Argon2id minimum: 19 MiB, two passes, one lane.
func hashPassword(password string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return "$argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash)
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// A valid dummy hash ensures unknown logins still incur the password-hash cost.
var dummyHash = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// CreateUser creates a unique login with a hashed password.
func (s *Store) CreateUser(login, password, role string) (User, error) {
	return s.createUser(login, password, role, false)
}

// Bootstrap creates the first administrator only when the user database is empty.
func (s *Store) Bootstrap(login, password string) (User, error) {
	return s.createUser(login, password, "admin", true)
}
func (s *Store) createUser(login, password, role string, bootstrap bool) (User, error) {
	login = NormalizeLogin(login)
	if err := validate(login, role, password); err != nil {
		return User{}, err
	}
	hash := hashPassword(password)
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if bootstrap {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
			return User{}, err
		}
		if count != 0 {
			return User{}, errors.New("bootstrap is only available for an empty user database")
		}
	}
	result, err := tx.Exec("INSERT INTO users(login,password_hash,role) VALUES(?,?,?) ON CONFLICT(login) DO NOTHING", login, hash, role)
	if err != nil {
		return User{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if n == 0 {
		return User{}, ErrConflict
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Login: login, Role: role, Enabled: true}, nil
}

// Users lists public identities without credentials.
func (s *Store) Users() ([]User, error) {
	rows, err := s.db.Query("SELECT id,login,role,enabled FROM users ORDER BY login")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	users := []User{}
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Login, &u.Role, &u.Enabled); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Update contains the access and credential changes an administrator can make.
type Update struct {
	Role     *string `json:"role,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
	Password *string `json:"password,omitempty"`
}

// UpdateUser applies changes atomically and revokes all sessions of the user.
func (s *Store) UpdateUser(id int64, update Update) (User, error) {
	var hash string
	if update.Password != nil {
		if err := validate("user", "user", *update.Password); err != nil {
			return User{}, err
		}
		hash = hashPassword(*update.Password)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var u User
	err = tx.QueryRow("SELECT id,login,role,enabled FROM users WHERE id=?", id).Scan(&u.ID, &u.Login, &u.Role, &u.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	wasAdmin := u.Enabled && u.Role == "admin"
	if update.Role != nil {
		if *update.Role != "admin" && *update.Role != "user" {
			return User{}, ErrInvalid
		}
		u.Role = *update.Role
	}
	if update.Enabled != nil {
		u.Enabled = *update.Enabled
	}
	if wasAdmin && (!u.Enabled || u.Role != "admin") {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM users WHERE role='admin' AND enabled=1").Scan(&count); err != nil {
			return User{}, err
		}
		if count <= 1 {
			return User{}, ErrLastAdmin
		}
	}
	if _, err = tx.Exec("UPDATE users SET role=?,enabled=? WHERE id=?", u.Role, u.Enabled, id); err != nil {
		return User{}, err
	}
	if hash != "" {
		if _, err = tx.Exec("UPDATE users SET password_hash=? WHERE id=?", hash, id); err != nil {
			return User{}, err
		}
	}
	// Every access/credential change revokes existing sessions, including sockets.
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Login verifies credentials and creates a new revocable session.
func (s *Store) Login(login, password string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow("SELECT id,login,role,enabled,password_hash FROM users WHERE login=?", NormalizeLogin(login)).Scan(&u.ID, &u.Login, &u.Role, &u.Enabled, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, "", err
	}
	if hash == "" {
		hash = dummyHash
	}
	if len(password) > 1024 {
		return User{}, "", ErrCredentials
	}
	valid := verifyPassword(hash, password)
	if !valid || err != nil || !u.Enabled {
		return User{}, "", ErrCredentials
	}
	bytes := make([]byte, 32)
	_, _ = rand.Read(bytes)
	token := base64.RawURLEncoding.EncodeToString(bytes)
	// Recheck the password/role state while inserting, so a concurrent admin reset
	// cannot create a session from credentials that have just been revoked.
	result, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at)
 SELECT ?,id,? FROM users WHERE id=? AND enabled=1 AND password_hash=? AND role=?`, tokenHash(token), time.Now().Add(SessionTTL).Unix(), u.ID, hash, u.Role)
	if err != nil {
		return User{}, "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return User{}, "", err
	}
	if n == 0 {
		return User{}, "", ErrCredentials
	}
	_, _ = s.db.Exec("DELETE FROM sessions WHERE expires_at<=?", time.Now().Unix())
	return u, token, nil
}

// Authenticate resolves an unexpired session for an enabled user.
func (s *Store) Authenticate(token string) (User, error) {
	if len(token) != 43 {
		return User{}, ErrSession
	}
	var u User
	err := s.db.QueryRow(`SELECT u.id,u.login,u.role,u.enabled FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=? AND s.expires_at>? AND u.enabled=1`, tokenHash(token), time.Now().Unix()).Scan(&u.ID, &u.Login, &u.Role, &u.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrSession
	}
	return u, err
}

// Logout revokes the supplied session.
func (s *Store) Logout(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE token_hash=?", tokenHash(token))
	return err
}
