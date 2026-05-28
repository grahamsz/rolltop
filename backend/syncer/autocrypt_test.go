package syncer

import (
	"context"
	"path/filepath"
	"testing"

	"mailmirror/backend/plugins"
	"mailmirror/backend/store"
)

const autocryptTestRaw = "From: Alice <alice@example.test>\r\nAutocrypt: addr=alice@example.test; prefer-encrypt=mutual; keydata=AQIDBAUGBwg=\r\nSubject: hello\r\n\r\nbody"

func TestDiscoverAutocryptHeadersStoresSenderKey(t *testing.T) {
	ctx := context.Background()
	db := autocryptTestStore(t)
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: db}
	if err := svc.discoverAutocryptHeaders(ctx, user.ID, []byte(autocryptTestRaw), "Alice <alice@example.test>"); err != nil {
		t.Fatal(err)
	}
	keys, err := db.ListAllContactPGPPublicKeysForEmails(ctx, user.ID, []string{"alice@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("stored key count = %d, want 1", len(keys))
	}
	if keys[0].Email != "alice@example.test" || !keys[0].IsPreferred || keys[0].PublicKeyArmored == "" {
		t.Fatalf("stored key = %+v", keys[0])
	}
}

func TestDiscoverAutocryptHeadersRequiresPluginAndMatchingFrom(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		enablePGP  bool
		parsedFrom string
	}{
		{name: "plugin disabled", enablePGP: false, parsedFrom: "Alice <alice@example.test>"},
		{name: "from mismatch", enablePGP: true, parsedFrom: "Mallory <mallory@example.test>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := autocryptTestStore(t)
			defer db.Close()
			user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.enablePGP {
				if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
					t.Fatal(err)
				}
			}
			svc := &Service{Store: db}
			if err := svc.discoverAutocryptHeaders(ctx, user.ID, []byte(autocryptTestRaw), tc.parsedFrom); err != nil {
				t.Fatal(err)
			}
			keys, err := db.ListAllContactPGPPublicKeysForEmails(ctx, user.ID, []string{"alice@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			if len(keys) != 0 {
				t.Fatalf("stored key count = %d, want 0", len(keys))
			}
		})
	}
}

func autocryptTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
