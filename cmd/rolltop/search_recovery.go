package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

// recoverMarkedSearchIndexes consumes durable writer-stall markers before any
// per-user Bleve handle is opened. SQLite completion flags are reset first, so
// an interrupted recovery can never leave a fresh index with rows marked done.
func recoverMarkedSearchIndexes(ctx context.Context, db *store.Store, searchSvc *search.Service, searchRoot string, users []store.User, now time.Time) ([]int64, error) {
	if db == nil || searchSvc == nil {
		return nil, fmt.Errorf("stalled search recovery is not configured")
	}
	recovered := make([]int64, 0)
	for _, user := range users {
		required, err := searchSvc.SearchIndexRecoveryRequired(user.ID)
		if err != nil {
			return recovered, fmt.Errorf("inspect search recovery marker for user %d: %w", user.ID, err)
		}
		if !required {
			continue
		}
		marked, err := db.MarkSearchVisibleMessagesPendingIndex(ctx, user.ID)
		if err != nil {
			return recovered, fmt.Errorf("mark search rows pending for user %d: %w", user.ID, err)
		}
		quarantine, err := search.QuarantinePerUserIndex(searchRoot, user.ID, now)
		if err != nil {
			return recovered, fmt.Errorf("quarantine stalled search index for user %d after marking %d rows pending: %w", user.ID, marked, err)
		}
		if err := searchSvc.ClearSearchIndexRecoveryRequired(user.ID); err != nil {
			return recovered, fmt.Errorf("clear search recovery marker for user %d after quarantine: %w", user.ID, err)
		}
		recovered = append(recovered, user.ID)
		log.Printf("recovered stalled search index user_id=%d pending_messages=%d quarantine=%q", user.ID, marked, quarantine.QuarantinePath)
	}
	return recovered, nil
}
