// File overview: Tenant isolation, authoritative read filtering, exclusions,
// and weighted coverage tests for plugin message similarity.

package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestSimilarMessagesRecentReadCandidatesUseAuthoritativeSQLiteState(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	owner, ownerAccount, ownerMailbox, ownerBlob := similarityTestMailbox(t, ctx, db, "owner@example.test")
	other, otherAccount, otherMailbox, otherBlob := similarityTestMailbox(t, ctx, db, "other@example.test")
	now := time.Now().UTC()
	current := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 1, "current-thread", now, false)
	recentRead := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 2, "recent-thread", now.Add(-24*time.Hour), true)
	sameThread := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 3, current.ThreadKey, now.Add(-2*time.Hour), true)
	unread := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 4, "unread-thread", now.Add(-3*time.Hour), false)
	oldRead := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 5, "old-thread", now.Add(-100*24*time.Hour), true)
	excluded := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 6, "excluded-thread", now.Add(-4*time.Hour), true)
	otherRead := similarityTestMessage(t, ctx, db, other, otherAccount, otherMailbox, otherBlob, 1, "other-thread", now.Add(-time.Hour), true)

	all := []store.MessageRecord{current, recentRead, sameThread, unread, oldRead, excluded, otherRead}
	for _, message := range all {
		indexed := message
		// Deliberately make Bleve disagree with SQLite. Candidate resolution must
		// still include recentRead and exclude unread.
		if message.ID == recentRead.ID {
			indexed.IsRead = false
		}
		if message.ID == unread.ID {
			indexed.IsRead = true
		}
		if err := searchService.IndexMessage(ctx, indexed, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := searchService.SimilarMessages(ctx, db, owner.ID, plugins.SimilarMessagesRequest{
		RecentRead:        &plugins.RecentReadCandidates{Since: now.Add(-120 * 24 * time.Hour), Limit: 5000},
		CurrentMessageID:  current.ID,
		ExcludeMessageIDs: []int64{excluded.ID},
		Terms: []plugins.SimilarityTerm{
			{Field: plugins.SimilarityFieldSubject, Text: "weekly", Weight: 2},
			{Field: plugins.SimilarityFieldBody, Text: "darkroom", Weight: 1},
		},
		Limit: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].MessageID != recentRead.ID {
		t.Fatalf("recent-read results = %+v, want only message %d", results, recentRead.ID)
	}
	if results[0].From != recentRead.FromAddr || results[0].ThreadKey != recentRead.ThreadKey || !results[0].Date.Equal(recentRead.Date) {
		t.Fatalf("hydrated result = %+v, want envelope from %+v", results[0], recentRead)
	}
}

func TestSimilarMessagesExplicitCandidatesAreOwnershipCheckedAndReportCoverage(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	owner, ownerAccount, ownerMailbox, ownerBlob := similarityTestMailbox(t, ctx, db, "explicit@example.test")
	other, otherAccount, otherMailbox, otherBlob := similarityTestMailbox(t, ctx, db, "foreign@example.test")
	now := time.Now().UTC()
	current := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 1, "current", now, false)
	owned := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 2, "owned", now.Add(-200*24*time.Hour), false)
	excluded := similarityTestMessage(t, ctx, db, owner, ownerAccount, ownerMailbox, ownerBlob, 3, "excluded", now, false)
	foreign := similarityTestMessage(t, ctx, db, other, otherAccount, otherMailbox, otherBlob, 1, "foreign", now, false)
	for _, message := range []store.MessageRecord{current, owned, excluded, foreign} {
		if err := searchService.IndexMessage(ctx, message, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := searchService.SimilarMessages(ctx, db, owner.ID, plugins.SimilarMessagesRequest{
		CandidateMessageIDs: []int64{owned.ID, excluded.ID, foreign.ID, owned.ID},
		CurrentMessageID:    current.ID,
		ExcludeMessageIDs:   []int64{excluded.ID},
		Terms: []plugins.SimilarityTerm{
			{Field: plugins.SimilarityFieldSubject, Text: "weekly", Weight: 3},
			{Field: plugins.SimilarityFieldBody, Text: "darkroom", Weight: 1},
			{Field: plugins.SimilarityFieldBody, Text: "missing", Weight: 2},
			{Field: "user_id", Text: fmt.Sprint(other.ID), Weight: 100},
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].MessageID != owned.ID {
		t.Fatalf("explicit results = %+v, want only owned message %d", results, owned.ID)
	}
	if math.Abs(results[0].WeightedTermCoverage-(2.0/3.0)) > 0.0001 {
		t.Fatalf("weighted coverage = %f, want 2/3", results[0].WeightedTermCoverage)
	}
	if results[0].MatchedTermCount != 2 || !slices.Equal(results[0].MatchedTerms, []string{"darkroom", "weekly"}) {
		t.Fatalf("matched terms = %v count=%d", results[0].MatchedTerms, results[0].MatchedTermCount)
	}
	if !slices.Equal(results[0].MatchedFields, []string{"body", "subject"}) {
		t.Fatalf("matched fields = %v", results[0].MatchedFields)
	}

	_, err = searchService.SimilarMessages(ctx, db, owner.ID, plugins.SimilarMessagesRequest{
		CandidateMessageIDs: []int64{owned.ID},
		CurrentMessageID:    foreign.ID,
		Terms:               []plugins.SimilarityTerm{{Field: plugins.SimilarityFieldSubject, Text: "weekly", Weight: 1}},
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign current message err = %v, want not found", err)
	}
}

func TestSimilarMessagesFromDomainRequiresEveryComponent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	owner, account, mailbox, blob := similarityTestMailbox(t, ctx, db, "domains@example.test")
	now := time.Now().UTC()
	current := similarityTestMessage(t, ctx, db, owner, account, mailbox, blob, 1, "current", now, false)
	exact := similarityTestMessage(t, ctx, db, owner, account, mailbox, blob, 2, "exact", now.Add(-time.Hour), false)
	sharedSuffix := similarityTestMessage(t, ctx, db, owner, account, mailbox, blob, 3, "shared-suffix", now.Add(-2*time.Hour), false)

	currentIndexed := current
	currentIndexed.FromAddr = "Current <current@inbox.test>"
	exactIndexed := exact
	exactIndexed.FromAddr = "Newsletter <news@example.com>"
	sharedSuffixIndexed := sharedSuffix
	sharedSuffixIndexed.FromAddr = "Newsletter <news@different.com>"
	for _, message := range []store.MessageRecord{currentIndexed, exactIndexed, sharedSuffixIndexed} {
		if err := searchService.IndexMessage(ctx, message, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := searchService.SimilarMessages(ctx, db, owner.ID, plugins.SimilarMessagesRequest{
		CandidateMessageIDs: []int64{exact.ID, sharedSuffix.ID},
		CurrentMessageID:    current.ID,
		Terms: []plugins.SimilarityTerm{
			{Field: plugins.SimilarityFieldFromDomain, Text: "example.com", Weight: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].MessageID != exact.ID {
		t.Fatalf("domain results = %+v, want only exact-domain message %d", results, exact.ID)
	}
	if results[0].MatchedTermCount != 1 || results[0].WeightedTermCoverage != 1 {
		t.Fatalf("domain coverage = %+v, want one fully matched term", results[0])
	}
}

func TestSimilarMessagesSubjectMultiTokenTermKeepsAnyComponentBehavior(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	owner, account, mailbox, blob := similarityTestMailbox(t, ctx, db, "subjects@example.test")
	now := time.Now().UTC()
	current := similarityTestMessage(t, ctx, db, owner, account, mailbox, blob, 1, "current", now, false)
	candidate := similarityTestMessage(t, ctx, db, owner, account, mailbox, blob, 2, "candidate", now.Add(-time.Hour), false)
	for _, message := range []store.MessageRecord{current, candidate} {
		if err := searchService.IndexMessage(ctx, message, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := searchService.SimilarMessages(ctx, db, owner.ID, plugins.SimilarMessagesRequest{
		CandidateMessageIDs: []int64{candidate.ID},
		CurrentMessageID:    current.ID,
		Terms: []plugins.SimilarityTerm{
			{Field: plugins.SimilarityFieldSubject, Text: "weekly absent", Weight: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].MessageID != candidate.ID {
		t.Fatalf("subject results = %+v, want partial multi-token match %d", results, candidate.ID)
	}
	if results[0].MatchedTermCount != 1 || results[0].WeightedTermCoverage != 1 {
		t.Fatalf("subject coverage = %+v, want existing any-component semantics", results[0])
	}
}

func similarityTestMailbox(t *testing.T, ctx context.Context, db *store.Store, email string) (store.User, store.MailAccount, store.Mailbox, store.BlobRecord) {
	t.Helper()
	user, err := db.CreateUser(ctx, email, email, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: email, Host: "imap.example.test", Port: 993,
		Username: email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: fmt.Sprintf("users/%d/similarity.eml", user.ID),
		SHA256: fmt.Sprintf("similarity-%d", user.ID), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox, blob
}

func similarityTestMessage(t *testing.T, ctx context.Context, db *store.Store, user store.User, account store.MailAccount, mailbox store.Mailbox, blob store.BlobRecord, uid uint32, threadKey string, date time.Time, read bool) store.MessageRecord {
	t.Helper()
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: fmt.Sprintf("<similarity-%d-%d@example.test>", user.ID, uid),
		ThreadKey:       threadKey,
		Subject:         "Weekly film update",
		FromAddr:        "Newsletter <news@example.test>",
		Date:            date,
		InternalDate:    date,
		UID:             uid,
		BlobPath:        blob.Path,
		BodyText:        "Darkroom camera notes",
		IsRead:          read,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}
