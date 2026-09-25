package rooms

import (
	"context"
	"errors"
	"testing"

	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/database"
)

func TestGrantsAndLifecycle(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	accounts := auth.New(db)
	admin, err := accounts.Bootstrap("admin", "abc")
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUser("user", "abc", "user")
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, 7000, 7999)
	ctx := context.Background()
	room, err := s.Create(ctx, " Team ", "7001")
	if err != nil {
		t.Fatal(err)
	}
	if room.Name != "Team" || room.State != "pending" || room.Bridge == room.Number {
		t.Fatal(room)
	}
	if ok, _ := s.Allowed(ctx, room.ID, user.ID); ok {
		t.Fatal("implicit user grant")
	}
	if ok, _ := s.Allowed(ctx, room.ID, admin.ID); !ok {
		t.Fatal("admin denied")
	}
	if err = s.SetGrants(ctx, room.ID, []int64{user.ID}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetGrants(ctx, room.ID, []int64{999}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if ok, _ := s.Allowed(ctx, room.ID, user.ID); !ok {
		t.Fatal("failed replacement lost grants")
	}
	if _, err = s.Create(ctx, "Duplicate", "7001"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, "Collision", "1001"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Deleting(ctx, room.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Result(ctx, room, "ready", nil); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(ctx, room.ID)
	if current.State != "deleting" {
		t.Fatal("stale completion undid deletion")
	}
	if ok, _ := s.Allowed(ctx, room.ID, user.ID); ok {
		t.Fatal("deleting room exposed")
	}
	if err = s.Result(ctx, current, "deleted", nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Allowed(ctx, room.ID, admin.ID); ok {
		t.Fatal("deleted room exposed")
	}
	replacement, err := s.Create(ctx, "Reused", "7001")
	if err != nil || replacement.Bridge == room.Bridge {
		t.Fatal("number cannot be safely reused", err)
	}
}

func TestFreshAndLegacyImportOnce(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{true: "legacy", false: "fresh"}[legacy], func(t *testing.T) {
			db, err := database.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			user, err := auth.New(db).CreateUser("user", "abc", "user")
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err = db.Exec("UPDATE app_metadata SET value='yes' WHERE key='legacy_room_pending'"); err != nil {
					t.Fatal(err)
				}
			}
			store := New(db, 7000, 7999)
			if err = store.ImportLegacy("1000"); err != nil {
				t.Fatal(err)
			}
			list, _ := store.All(context.Background())
			if legacy && len(list) != 1 || !legacy && len(list) != 0 {
				t.Fatal(list)
			}
			if legacy {
				if ok, _ := store.Allowed(context.Background(), "legacy", user.ID); !ok {
					t.Fatal("legacy access lost")
				}
				if err = store.SetGrants(context.Background(), "legacy", nil); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.ImportLegacy("1000"); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if ok, _ := store.Allowed(context.Background(), "legacy", user.ID); ok {
					t.Fatal("restart restored revoked grant")
				}
			}
		})
	}
}

func TestExpandedRoomRange(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := New(db, 100, 9999)
	for _, number := range []string{"100", "999", "1000", "6999", "8000", "9999"} {
		if _, err := s.Create(context.Background(), "Room", number); err != nil {
			t.Errorf("%s: %v", number, err)
		}
	}
	for _, number := range []string{"99", "10000"} {
		if _, err := s.Create(context.Background(), "Room", number); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", number, err)
		}
	}
}
