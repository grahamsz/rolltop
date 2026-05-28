package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMailIdentityAutocryptDefaultsAndUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, user.ID, Contact{
		DisplayName: "Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []ContactEmail{{Email: "me@example.test", IsPrimary: true}},
	}); err != nil {
		t.Fatal(err)
	}
	identities, err := db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 {
		t.Fatalf("identity count = %d", len(identities))
	}
	if !identities[0].AutocryptEnabled {
		t.Fatalf("new identity AutocryptEnabled = false, want true")
	}
	next := identities[0]
	next.AutocryptEnabled = false
	updated, err := db.UpdateMailIdentityForUser(ctx, user.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AutocryptEnabled {
		t.Fatalf("updated identity AutocryptEnabled = true, want false")
	}
}
