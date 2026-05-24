package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptString(t *testing.T) {
	key := []byte("12345678901234567890123456789012")
	ciphertext, err := EncryptString(key, "imap password")
	if err != nil {
		t.Fatal(err)
	}
	if ciphertext == "imap password" || !strings.HasPrefix(ciphertext, "v1:") {
		t.Fatalf("unexpected ciphertext %q", ciphertext)
	}
	plaintext, err := DecryptString(key, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "imap password" {
		t.Fatalf("got %q", plaintext)
	}
}
