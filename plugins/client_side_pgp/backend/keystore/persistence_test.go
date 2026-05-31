package keystore

import (
	"context"
	"path/filepath"
	"testing"

	"rolltop/backend/store"
)

func TestIdentityPrivateKeyCanUseBrowserStorageMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "pgp-browser@example.test", "PGP Browser", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := db.CreateMailIdentityForUser(ctx, user.ID, store.MailIdentity{Email: user.Email, DisplayName: "PGP Browser"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := UpsertIdentityPrivateKey(ctx, db, store.IdentityPGPPrivateKey{
		UserID:              user.ID,
		IdentityID:          identity.ID,
		Label:               user.Email,
		Fingerprint:         "abc123",
		KeyID:               "abc123",
		UserIDs:             "PGP Browser <pgp-browser@example.test>",
		PublicKeyArmored:    "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nx\n-----END PGP PUBLIC KEY BLOCK-----",
		PrivateKeyStorage:   "browser",
		IsActiveSigning:     true,
		IsActiveEncryption:  true,
		EncryptedPrivateKey: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.PrivateKeyStorage != "browser" || saved.EncryptedPrivateKey != "" {
		t.Fatalf("saved key storage = %#v", saved)
	}
	active, err := ActiveIdentityPublicKeyForUser(ctx, db, user.ID, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.PublicKeyArmored == "" || active.PrivateKeyStorage != "browser" {
		t.Fatalf("active public key = %#v", active)
	}
}

func TestContactPublicKeysMustMatchContactEmail(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "pgp-contact@example.test", "PGP Contact", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Alice",
		Emails:      []store.ContactEmail{{Email: "alice@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertContactPublicKey(ctx, db, store.ContactPGPPublicKey{
		UserID:           user.ID,
		ContactID:        contact.ID,
		Email:            "mallory@example.test",
		PublicKeyArmored: "mallory public key",
	}); !store.IsNotFound(err) {
		t.Fatalf("mismatched PGP upsert err = %v, want not found", err)
	}
	saved, err := UpsertContactPublicKey(ctx, db, store.ContactPGPPublicKey{
		UserID:           user.ID,
		ContactID:        contact.ID,
		Email:            "alice@example.test",
		PublicKeyArmored: "alice public key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.NormalizedEmail != "alice@example.test" || saved.PublicKeyArmored != "alice public key" {
		t.Fatalf("saved PGP key = %+v", saved)
	}
	keys, err := ListAllPublicKeysForEmails(ctx, db, user.ID, []string{"alice@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != saved.ID {
		t.Fatalf("keys = %+v, want saved key", keys)
	}
}

func TestNormalizeContactPublicKeyDefaultsSourceKind(t *testing.T) {
	key := normalizeContactPublicKey(store.ContactPGPPublicKey{
		Email:            "Alice@Example.test",
		PublicKeyArmored: " key ",
	})
	if key.NormalizedEmail != "alice@example.test" || key.SourceKind != "manual" || key.PublicKeyArmored != "key" {
		t.Fatalf("normalized key = %+v", key)
	}
}

func TestNormalizeContactPublicKeyKeepsSourceMetadata(t *testing.T) {
	key := normalizeContactPublicKey(store.ContactPGPPublicKey{
		Email:            "alice@example.test",
		PublicKeyArmored: "key",
		SourceKind:       " Autocrypt-Gossip ",
		SourceDetail:     " message.eml ",
	})
	if key.SourceKind != "autocrypt-gossip" || key.SourceDetail != "message.eml" {
		t.Fatalf("source metadata = %q %q", key.SourceKind, key.SourceDetail)
	}
}
