package search

import (
	"errors"
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

func TestQuarantinePerUserIndexRollsBackWhenRenameCannotBePersisted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "users")
	indexPath := filepath.Join(root, "17", "bleve")
	if err := os.MkdirAll(indexPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexPath, "owner"), []byte("owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("directory sync failed")
	syncCalls := 0
	_, err := quarantinePerUserIndexWithSync(root, 17, time.Now(), func(path string) error {
		syncCalls++
		if path != filepath.Dir(indexPath) {
			t.Fatalf("synced directory = %q, want %q", path, filepath.Dir(indexPath))
		}
		if syncCalls == 1 {
			if _, statErr := os.Stat(indexPath); !os.IsNotExist(statErr) {
				t.Fatalf("live index existed before quarantine sync: %v", statErr)
			}
			return persistErr
		}
		return nil
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("quarantine error = %v, want persistence failure", err)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want quarantine and rollback sync", syncCalls)
	}
	if raw, statErr := os.ReadFile(filepath.Join(indexPath, "owner")); statErr != nil || string(raw) != "owner" {
		t.Fatalf("live index was not restored: %q, %v", raw, statErr)
	}
	quarantines, globErr := filepath.Glob(indexPath + ".quarantine-*")
	if globErr != nil || len(quarantines) != 0 {
		t.Fatalf("quarantine remained after rollback: %v, %v", quarantines, globErr)
	}
}
