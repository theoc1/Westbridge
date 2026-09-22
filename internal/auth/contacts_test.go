package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPersonalContactsPersist(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "book.db")
	store, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.CreateUser("alice", "abc", "user")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.CreateUser("bob", "abc", "admin")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := store.SaveContact(ctx, alice.ID, 0, " Alice's friend ", "(100)-2")
	if err != nil || contact.Number != "1002" || contact.Name != "Alice's friend" {
		t.Fatal(contact, err)
	}
	if _, err := store.SaveContact(ctx, alice.ID, 0, "Duplicate", "1002"); !errors.Is(err, ErrContactDuplicate) {
		t.Fatal(err)
	}
	if _, err := store.SaveContact(ctx, bob.ID, 0, "Bob's friend", "1002"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveContact(ctx, bob.ID, contact.ID, "Stolen", "1003"); !errors.Is(err, ErrContactMissing) {
		t.Fatal(err)
	}
	if err := store.DeleteContact(ctx, bob.ID, contact.ID); !errors.Is(err, ErrContactMissing) {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ name, number string }{{"", "1002"}, {"Name", "1002@other"}, {"Name\nInjected", "1002"}} {
		if _, err := store.SaveContact(ctx, alice.ID, 0, invalid.name, invalid.number); !errors.Is(err, ErrContactInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveContact(ctx, alice.ID, contact.ID, "Renamed", "1003"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	entries, err := store.Contacts(ctx, alice.ID)
	if err != nil || len(entries) != 1 || entries[0].Name != "Renamed" || entries[0].Number != "1003" {
		t.Fatal(entries, err)
	}
	if err := store.DeleteContact(ctx, alice.ID, contact.ID); err != nil {
		t.Fatal(err)
	}
	entries, err = store.Contacts(ctx, alice.ID)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
