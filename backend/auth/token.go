// File overview: Session token generation and hashing helpers.

package auth

import (
	"crypto/rand"
	"encoding/base64"
	"io"
)

// NewOpaqueToken returns a cryptographically random URL-safe token for sessions and CSRF-style secrets.
func NewOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
