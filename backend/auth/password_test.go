// File overview: Tests for password hashing and verification behavior.

package auth

import "testing"

func TestArgon2idPasswordHash(t *testing.T) {
	hash, err := HashPasswordWithParams("correct horse battery staple", Argon2idParams{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected password to verify")
	}
	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password verified")
	}
}
