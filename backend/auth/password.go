// File overview: Password hashing and verification helpers.

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2idParams holds the tunable password-hashing parameters used by HashPasswordWithParams.
type Argon2idParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultArgon2idParams = Argon2idParams{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// HashPassword derives a password hash using the default interactive parameters for local Rolltop users.
func HashPassword(password string) (string, error) {
	return HashPasswordWithParams(password, DefaultArgon2idParams)
}

// HashPasswordWithParams exists for tests and migrations that need deterministic or lower-cost password hashing parameters.
func HashPasswordWithParams(password string, p Argon2idParams) (string, error) {
	if password == "" {
		return "", errors.New("password cannot be empty")
	}
	salt := make([]byte, p.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		p.Memory, p.Iterations, p.Parallelism, enc.EncodeToString(salt), enc.EncodeToString(hash)), nil
}

// VerifyPassword checks a candidate password against the stored hash format used by HashPassword.
func VerifyPassword(encoded, password string) (bool, error) {
	p, salt, expected, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(expected)))
	if subtle.ConstantTimeCompare(actual, expected) == 1 {
		return true, nil
	}
	return false, nil
}

func decodeHash(encoded string) (Argon2idParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return Argon2idParams{}, nil, nil, errors.New("invalid password hash format")
	}
	var p Argon2idParams
	for _, kv := range strings.Split(parts[2], ",") {
		keyValue := strings.SplitN(kv, "=", 2)
		if len(keyValue) != 2 {
			return Argon2idParams{}, nil, nil, errors.New("invalid password hash parameters")
		}
		n, err := strconv.ParseUint(keyValue[1], 10, 32)
		if err != nil {
			return Argon2idParams{}, nil, nil, err
		}
		switch keyValue[0] {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			p.Parallelism = uint8(n)
		default:
			return Argon2idParams{}, nil, nil, errors.New("unknown password hash parameter")
		}
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[3])
	if err != nil {
		return Argon2idParams{}, nil, nil, err
	}
	hash, err := enc.DecodeString(parts[4])
	if err != nil {
		return Argon2idParams{}, nil, nil, err
	}
	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(hash))
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 || p.SaltLength == 0 || p.KeyLength == 0 {
		return Argon2idParams{}, nil, nil, errors.New("invalid password hash parameters")
	}
	return p, salt, hash, nil
}
