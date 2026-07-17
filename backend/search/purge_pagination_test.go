package search

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestPurgeMailboxWithProgressCrossesBatchBoundary(t *testing.T) {
	for _, test := range []struct {
		name          string
		documents     int
		wantBatches   []int
		wantRemaining []int
	}{
		{
			name: "exact reported stall count", documents: 251,
			wantBatches: []int{100, 100, 51}, wantRemaining: []int{151, 51, 0},
		},
		{
			name: "crosses old batch boundary", documents: 501,
			wantBatches: []int{100, 100, 100, 100, 100, 1}, wantRemaining: []int{401, 301, 201, 101, 1, 0},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			service, err := OpenPerUser(filepath.Join(t.TempDir(), "users"))
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			const (
				userID    = int64(1)
				mailboxID = int64(10)
			)
			items := make([]MessageIndexDocument, 0, test.documents+1)
			for id := int64(1); id <= int64(test.documents); id++ {
				items = append(items, MessageIndexDocument{Message: store.MessageRecord{
					ID: id, UserID: userID, MailboxID: mailboxID,
					Subject: "purge pagination", Date: time.Unix(id, 0).UTC(),
				}})
			}
			items = append(items, MessageIndexDocument{Message: store.MessageRecord{
				ID: int64(test.documents + 1), UserID: userID, MailboxID: mailboxID + 1,
				Subject: "keep other mailbox", Date: time.Unix(int64(test.documents+1), 0).UTC(),
			}})
			if err := service.IndexMessages(ctx, items); err != nil {
				t.Fatal(err)
			}

			var batches, remaining []int
			deleted, err := service.PurgeMailboxWithProgress(ctx, userID, mailboxID, func(n int) error {
				batches = append(batches, n)
				count, err := service.CountMailboxMessages(ctx, userID, mailboxID)
				if err != nil {
					return err
				}
				remaining = append(remaining, count)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if deleted != test.documents {
				t.Fatalf("deleted=%d, want %d", deleted, test.documents)
			}
			if !reflect.DeepEqual(batches, test.wantBatches) {
				t.Fatalf("progress batches=%v, want %v", batches, test.wantBatches)
			}
			if !reflect.DeepEqual(remaining, test.wantRemaining) {
				t.Fatalf("remaining counts from callbacks=%v, want %v", remaining, test.wantRemaining)
			}
			if count, err := service.CountMailboxMessages(ctx, userID, mailboxID+1); err != nil || count != 1 {
				t.Fatalf("other mailbox count=%d err=%v, want 1/nil", count, err)
			}
		})
	}
}
