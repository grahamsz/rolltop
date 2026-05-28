package autocrypt

import "testing"

const armoredTestPublicKey = `-----BEGIN PGP PUBLIC KEY BLOCK-----

AQIDBAUGBwg=
-----END PGP PUBLIC KEY BLOCK-----`

func TestKeyDataRoundTrip(t *testing.T) {
	keyData, ok := KeyDataFromArmoredPublicKey(armoredTestPublicKey)
	if !ok {
		t.Fatal("KeyDataFromArmoredPublicKey returned false")
	}
	if keyData != "AQIDBAUGBwg=" {
		t.Fatalf("keydata = %q", keyData)
	}
	armored, ok := ArmoredPublicKeyFromKeyData(keyData)
	if !ok {
		t.Fatal("ArmoredPublicKeyFromKeyData returned false")
	}
	roundTrip, ok := KeyDataFromArmoredPublicKey(armored)
	if !ok || roundTrip != keyData {
		t.Fatalf("round trip keydata = %q ok=%v, want %q", roundTrip, ok, keyData)
	}
}

func TestParseHeaderValue(t *testing.T) {
	header, ok := ParseHeaderValue(`addr=alice@example.test; prefer-encrypt=mutual; keydata=AQIDBAUGBwg=`)
	if !ok {
		t.Fatal("ParseHeaderValue returned false")
	}
	if header.Addr != "alice@example.test" || header.PreferEncrypt != "mutual" || header.KeyData != "AQIDBAUGBwg=" {
		t.Fatalf("header = %+v", header)
	}
	if header.PublicKey == "" {
		t.Fatalf("header missing armored public key: %+v", header)
	}
}

func TestParseHeaderValueRejectsInvalidKeyData(t *testing.T) {
	if _, ok := ParseHeaderValue(`addr=alice@example.test; keydata=not-base64!`); ok {
		t.Fatal("ParseHeaderValue accepted invalid keydata")
	}
}
