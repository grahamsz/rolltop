package store

import "testing"

func TestNormalizeContactPGPPublicKeyDefaultsSourceKind(t *testing.T) {
	key := normalizeContactPGPPublicKey(ContactPGPPublicKey{
		Email:            "alice@example.test",
		PublicKeyArmored: "armored",
	})
	if key.SourceKind != "manual" {
		t.Fatalf("source kind = %q, want manual", key.SourceKind)
	}
}

func TestNormalizeContactPGPPublicKeyKeepsSourceMetadata(t *testing.T) {
	key := normalizeContactPGPPublicKey(ContactPGPPublicKey{
		Email:            "alice@example.test",
		PublicKeyArmored: "armored",
		SourceKind:       "Autocrypt",
		SourceDetail:     "alice@example.test",
	})
	if key.SourceKind != "autocrypt" || key.SourceDetail != "alice@example.test" {
		t.Fatalf("normalized source metadata = %+v", key)
	}
}
