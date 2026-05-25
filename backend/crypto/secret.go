// File overview: AES-GCM credential encryption and decryption.

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// EncryptString encrypts a credential with the process master key for storage in SQLite.
func EncryptString(key []byte, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), nil)
	enc := base64.RawStdEncoding
	return "v1:" + enc.EncodeToString(nonce) + ":" + enc.EncodeToString(ciphertext), nil
}

// DecryptString decrypts a stored credential and returns an error when the master key no longer matches.
func DecryptString(key []byte, value string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("encryption key must be 32 bytes")
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return "", errors.New("unsupported encrypted value format")
	}
	enc := base64.RawStdEncoding
	nonce, err := enc.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := enc.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("decrypt encrypted value")
	}
	return string(plaintext), nil
}

// TokenHash derives a stable SHA-256 token digest so session cookies are never stored in plaintext.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
