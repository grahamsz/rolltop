// File overview: Tests for user-scoped blob path behavior.

package blob

import (
	"strings"
	"testing"
)

func TestSaveRawMessageUsesUserDataDirectoryLayout(t *testing.T) {
	store := New(t.TempDir())
	saved, err := store.SaveRawMessage(42, 7, "INBOX", 99, []byte("From: a@example.test\r\n\r\nhello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Path, "users/42/blobs/accounts/7/mailboxes/INBOX/") {
		t.Fatalf("blob path = %q", saved.Path)
	}
	if f, err := store.OpenUserBlob(42, saved.Path); err != nil {
		t.Fatalf("open saved blob: %v", err)
	} else {
		_ = f.Close()
	}
	if _, err := store.OpenUserBlob(43, saved.Path); err == nil {
		t.Fatal("other user opened blob")
	}
}

func TestOpenUserBlobRejectsOldLayoutUserPath(t *testing.T) {
	oldPath := "blobs/users/9/accounts/1/mailboxes/INBOX/uid-1.eml"
	if userBlobPathAllowed(9, oldPath) {
		t.Fatalf("old layout path was allowed: %s", oldPath)
	}
	store := New(t.TempDir())
	if _, err := store.OpenUserBlob(9, oldPath); err == nil {
		t.Fatalf("opened old layout path: %s", oldPath)
	}
}
