package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuarantinePerUserIndexMovesOnlyTargetTenantAndRestores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "users")
	ownerIndex := filepath.Join(root, "17", "bleve")
	otherIndex := filepath.Join(root, "23", "bleve")
	for _, path := range []string{ownerIndex, otherIndex} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ownerIndex, "owner"), []byte("owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherIndex, "other"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}

	quarantine, err := QuarantinePerUserIndex(root, 17, time.Date(2026, 7, 15, 20, 30, 1, 123, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if quarantine.IndexPath != ownerIndex {
		t.Fatalf("index path = %q, want %q", quarantine.IndexPath, ownerIndex)
	}
	if !strings.HasPrefix(quarantine.QuarantinePath, ownerIndex+".quarantine-") {
		t.Fatalf("quarantine path = %q", quarantine.QuarantinePath)
	}
	if _, err := os.Stat(ownerIndex); !os.IsNotExist(err) {
		t.Fatalf("live owner index still exists: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(quarantine.QuarantinePath, "owner")); err != nil || string(raw) != "owner" {
		t.Fatalf("quarantined owner marker = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(otherIndex, "other")); err != nil || string(raw) != "other" {
		t.Fatalf("other tenant index changed: %q, %v", raw, err)
	}

	if err := RestoreQuarantinedIndex(quarantine); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(ownerIndex, "owner")); err != nil || string(raw) != "owner" {
		t.Fatalf("restored owner marker = %q, %v", raw, err)
	}
}

func TestQuarantinePerUserIndexRejectsInvalidUser(t *testing.T) {
	if _, err := QuarantinePerUserIndex(t.TempDir(), 0, time.Now()); err == nil {
		t.Fatal("invalid user ID was accepted")
	}
}

func TestQuarantinePerUserIndexWithoutExistingIndexIsReversibleNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "users")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	quarantine, err := QuarantinePerUserIndex(root, 42, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if quarantine.IndexPath != filepath.Join(root, "42", "bleve") || quarantine.QuarantinePath != "" {
		t.Fatalf("quarantine = %+v", quarantine)
	}
	if err := RestoreQuarantinedIndex(quarantine); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantinePerUserIndexRejectsSymlinkedTenantDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "users")
	target := filepath.Join(t.TempDir(), "tenant")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "17")); err != nil {
		t.Fatal(err)
	}
	if _, err := QuarantinePerUserIndex(root, 17, time.Now()); err == nil || !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("symlinked tenant directory error = %v", err)
	}
}
