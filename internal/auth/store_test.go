package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistenceAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.Bootstrap(" Admin ", "long-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Login != "admin" {
		t.Fatal(admin)
	}
	if _, err = s.Bootstrap("other", "long-password-123"); err == nil {
		t.Fatal("second bootstrap allowed")
	}
	user, err := s.CreateUser("alice", "alice-password-123", "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateUser("ALICE", "another-password", "user"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	_, token, err := s.Login("ALICE", "alice-password-123")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.db.QueryRow("SELECT token_hash FROM sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || len(stored) != 64 {
		t.Fatal("raw session stored")
	}
	if err = s.db.QueryRow("SELECT password_hash FROM users WHERE id=?", user.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatal("password not hashed")
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got, err := s.Authenticate(token); err != nil || got.ID != user.ID {
		t.Fatal(got, err)
	}
	password := "replacement-password"
	if _, err = s.UpdateUser(user.ID, Update{Password: &password}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(token); !errors.Is(err, ErrSession) {
		t.Fatal("old session survived password reset", err)
	}
	if _, _, err = s.Login("alice", "alice-password-123"); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	_, token, err = s.Login("alice", password)
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	if _, err = s.UpdateUser(user.ID, Update{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(token); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	if _, _, err = s.Login("alice", password); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	if _, err = s.UpdateUser(admin.ID, Update{Enabled: &enabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatal(err)
	}
	role := "user"
	if _, err = s.UpdateUser(admin.ID, Update{Role: &role}); !errors.Is(err, ErrLastAdmin) {
		t.Fatal(err)
	}
}
func TestSessionExpiryLogoutAndValidation(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.Bootstrap("admin", "short"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.Bootstrap("admin", "long-password-123"); err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"admin", "missing"} {
		if _, _, err = s.Login(login, "wrong"); !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	_, token, err := s.Login("admin", "long-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE sessions SET expires_at=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(token); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	_, token, err = s.Login("admin", "long-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(token); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
}
