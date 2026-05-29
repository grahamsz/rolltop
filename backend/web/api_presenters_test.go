package web

import (
	"testing"

	"rolltop/backend/store"
)

func TestAPIContactPGPKeyFromStoreIncludesSourceMetadata(t *testing.T) {
	key := apiContactPGPKeyFromStore(store.ContactPGPPublicKey{
		ID:               7,
		ContactID:        11,
		Email:            "alice@example.test",
		Label:            "Alice",
		Fingerprint:      "0011",
		KeyID:            "AABB",
		UserIDs:          "Alice <alice@example.test>",
		PublicKeyArmored: "armored",
		SourceKind:       "autocrypt",
		SourceDetail:     "alice@example.test",
		IsPreferred:      true,
	})
	if key.SourceKind != "autocrypt" || key.SourceDetail != "alice@example.test" {
		t.Fatalf("api key = %+v", key)
	}
}
