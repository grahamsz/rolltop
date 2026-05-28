package web

import (
	"context"
	"path/filepath"
	"testing"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestPGPPublicKeyAttachmentCandidateRequiresPluginSmallASC(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	view := threadMessageView{
		Message:     store.MessageRecord{ID: 1, UserID: user.ID},
		SenderEmail: "alice@example.test",
		Attachments: []store.Attachment{
			{ID: 1, UserID: user.ID, MessageID: 1, Filename: "alice.asc", ContentType: "application/pgp-keys", Size: 2481},
			{ID: 2, UserID: user.ID, MessageID: 1, Filename: "large.asc", ContentType: "application/pgp-keys", Size: 20 * 1024},
			{ID: 3, UserID: user.ID, MessageID: 1, Filename: "OpenPGP_signature.asc", ContentType: "application/pgp-signature", Size: 677},
			{ID: 4, UserID: user.ID, MessageID: 1, Filename: "note.txt", ContentType: "text/plain", Size: 12},
		},
	}
	disabled := server.apiThreadMessages(ctx, user.ID, []threadMessageView{view})
	if disabled[0].Attachments[0].PGPPublicKeyCandidate {
		t.Fatal("PGP attachment candidate was exposed while plugin is disabled")
	}
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	enabled := server.apiThreadMessages(ctx, user.ID, []threadMessageView{view})
	if !enabled[0].Attachments[0].PGPPublicKeyCandidate {
		t.Fatal("PGP public-key attachment was not marked as a candidate")
	}
	for _, att := range enabled[0].Attachments[1:] {
		if att.PGPPublicKeyCandidate {
			t.Fatalf("attachment %q was unexpectedly marked as a PGP key candidate", att.Filename)
		}
	}
}
