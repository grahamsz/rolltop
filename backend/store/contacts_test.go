package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestContactsAreScopedByUser(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "contacts@example.test", "Contacts", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-contacts@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, Contact{
		DisplayName: "Shared Name",
		Emails:      []ContactEmail{{Email: "shared@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, other.ID, Contact{
		DisplayName: "Other Shared",
		Emails:      []ContactEmail{{Email: "shared@example.test", IsPrimary: true}},
	}); err != nil {
		t.Fatal(err)
	}
	found, err := db.GetContactByEmailForUser(ctx, user.ID, "shared@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != contact.ID || found.DisplayName != "Shared Name" {
		t.Fatalf("found = %+v", found)
	}
	items, err := db.AutocompleteContactsForUser(ctx, user.ID, "shared", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "Shared Name" {
		t.Fatalf("autocomplete = %+v", items)
	}
}

func TestContactIconsAreScopedByUser(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "icon@example.test", "Icon", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-icon@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, Contact{
		DisplayName: "Icon Contact",
		Emails:      []ContactEmail{{Email: "icon@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID,
		Kind:   "contact_icon",
		Path:   "users/1/blobs/contacts/1/icons/icon.png",
		SHA256: "hash",
		Size:   4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetContactIcon(ctx, user.ID, contact.ID, blob.ID, "image/png", "icon.png", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetContactIconForUser(ctx, other.ID, contact.ID); !IsNotFound(err) {
		t.Fatalf("other user icon err = %v", err)
	}
	if _, err := db.GetContactIconByEmailForUser(ctx, other.ID, "icon@example.test"); !IsNotFound(err) {
		t.Fatalf("other user email icon err = %v", err)
	}
}
