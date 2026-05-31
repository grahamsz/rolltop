// File overview: Tests for search indexing, query behavior, tenant isolation, and highlighting.

package search

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestOpenPerUserKeepsDuplicateMessageIDsSeparate(t *testing.T) {
	ctx := context.Background()
	svc, err := OpenPerUser(filepath.Join(t.TempDir(), "users"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "alpha", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 2, Subject: "beta", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}

	ids, err := svc.Search(ctx, 1, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("user 1 ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("user 2 alpha ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "beta", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("user 2 beta ids = %v", ids)
	}
}

func TestCountMailboxMessagesIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, MailboxID: 10, Subject: "one", Date: time.Now()},
		{ID: 2, UserID: 1, MailboxID: 10, Subject: "two", Date: time.Now()},
		{ID: 3, UserID: 1, MailboxID: 20, Subject: "three", Date: time.Now()},
		{ID: 4, UserID: 2, MailboxID: 10, Subject: "four", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	count, err := svc.CountMailboxMessages(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("user 1 mailbox 10 count = %d", count)
	}
	count, err = svc.CountMailboxMessages(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 1 mailbox 20 count = %d", count)
	}
	count, err = svc.CountMailboxMessages(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 2 mailbox 10 count = %d", count)
	}
}

func TestCountUserMessagesIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "one", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "two", Date: time.Now()},
		{ID: 3, UserID: 2, Subject: "three", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	count, err := svc.CountUserMessages(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("user 1 count = %d", count)
	}
	count, err = svc.CountUserMessages(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 2 count = %d", count)
	}
}

func TestPurgeMailboxSearchIndexIsTenantAndMailboxScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, MailboxID: 10, Subject: "purge me", Date: time.Now()},
		{ID: 2, UserID: 1, MailboxID: 10, Subject: "purge me too", Date: time.Now()},
		{ID: 3, UserID: 1, MailboxID: 20, Subject: "keep same user", Date: time.Now()},
		{ID: 4, UserID: 2, MailboxID: 10, Subject: "keep other user", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.MailboxMessageIDs(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || !ids[1] || !ids[2] {
		t.Fatalf("mailbox ids before purge = %#v", ids)
	}
	deleted, err := svc.PurgeMailbox(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d", deleted)
	}
	for _, tt := range []struct {
		userID    int64
		mailboxID int64
		want      int
	}{
		{userID: 1, mailboxID: 10, want: 0},
		{userID: 1, mailboxID: 20, want: 1},
		{userID: 2, mailboxID: 10, want: 1},
	} {
		count, err := svc.CountMailboxMessages(ctx, tt.userID, tt.mailboxID)
		if err != nil {
			t.Fatal(err)
		}
		if count != tt.want {
			t.Fatalf("count user=%d mailbox=%d = %d, want %d", tt.userID, tt.mailboxID, count, tt.want)
		}
	}
}

func TestSearchRecentStillAppliesTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "alpha older", BodyText: "needle", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "beta newest", BodyText: "not a match", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "alpha newer", BodyText: "needle", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 4, UserID: 2, Subject: "alpha other tenant", BodyText: "needle", Date: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchAppliesAttachmentOperator(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "older attachment", HasAttachments: true, Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, []AttachmentDoc{{Filename: "one.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 2, UserID: 1, Subject: "newer no attachment", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 3, UserID: 1, Subject: "newer attachment", HasAttachments: true, Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, []AttachmentDoc{{Filename: "three.txt"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "has:attachment", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchStarredOperatorsAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "alpha starred", IsStarred: true, Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "alpha plain", IsStarred: false, Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 2, Subject: "alpha other tenant", IsStarred: true, Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "alpha is:starred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "alpha is:notstarred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("not starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 2, "alpha is:starred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("other tenant starred ids = %v", ids)
	}
}

func TestSearchLanguageOperatorIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "bonjour", BodyText: "facture", LanguageCode: "fr", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "hello", BodyText: "invoice", LanguageCode: "en", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 2, Subject: "bonjour other tenant", BodyText: "facture", LanguageCode: "fr", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "lang:fr", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("fr ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "lang:fr facture", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("combined ids = %v", ids)
	}
}

func TestSearchMatchesCompoundedSpacing(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "River Rise notice", BodyText: "water level update", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "riverrise", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "riverrse", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("typo ids = %v", ids)
	}
}

func TestSearchPlainMultiWordRequiresAllTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "dark room setup", BodyText: "lighting notes", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "dark hallway", BodyText: "single-term match only", Date: time.Now()},
		{ID: 3, UserID: 1, Subject: "guest room", BodyText: "single-term match only", Date: time.Now()},
		{ID: 4, UserID: 1, Subject: "dark hallway", BodyText: "a separate room reference", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "dark room", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[1] || !got[4] {
		t.Fatalf("expected both-term matches, ids = %v", ids)
	}
	if got[2] || got[3] {
		t.Fatalf("single-term matches should not be returned, ids = %v", ids)
	}
}

func TestSearchHitsReportsActualMatchedTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "dark room setup", BodyText: "lighting notes", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "dark hallway", BodyText: "single-term match only", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := svc.SearchHitsWithOptions(ctx, 1, "darkk room", 10, 0, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	terms := map[string]bool{}
	for _, term := range hits[0].Terms {
		terms[term] = true
	}
	if !terms["dark"] || !terms["room"] {
		t.Fatalf("terms = %v", hits[0].Terms)
	}
}

func TestExplainMessageWithOptionsReturnsScoreLocationsAndRawTree(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msg := store.MessageRecord{ID: 10, UserID: 1, Subject: "housing report", BodyText: "The committee discussed housing policy.", Date: time.Now()}
	if err := svc.IndexMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	result, ok, err := svc.ExplainMessageWithOptions(ctx, 1, 10, "housing", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected message to match")
	}
	if result.Score <= 0 {
		t.Fatalf("score = %f", result.Score)
	}
	if result.Raw == nil {
		t.Fatal("expected raw score explanation")
	}
	if len(result.FieldMatches) == 0 {
		t.Fatalf("field matches = %#v", result.FieldMatches)
	}
	if len(result.QueryTerms) == 0 {
		t.Fatalf("query terms = %#v", result.QueryTerms)
	}
	if len(result.TermContributions) == 0 {
		t.Fatalf("term contributions = %#v", result.TermContributions)
	}
	found := false
	for _, match := range result.FieldMatches {
		for _, term := range match.Terms {
			if term == "housing" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("field matches did not include housing: %#v", result.FieldMatches)
	}
	if _, ok, err := svc.ExplainMessageWithOptions(ctx, 2, 10, "housing", SearchOptions{}); err != nil || ok {
		t.Fatalf("cross-user explain ok=%v err=%v", ok, err)
	}
}

func TestExplainMessagesWithOptionsReturnsBestCandidate(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	messages := []store.MessageRecord{
		{ID: 20, UserID: 1, Subject: "checking in", BodyText: "No searched term here.", Date: time.Now()},
		{ID: 21, UserID: 1, Subject: "checking in", BodyText: "Nick mentioned the fund notice twice. Nick is available today.", Date: time.Now()},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	result, ok, err := svc.ExplainMessagesWithOptions(ctx, 1, []int64{20, 21}, "nick", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || result.ID != 21 {
		t.Fatalf("result ok=%v id=%d", ok, result.ID)
	}
	if len(result.QueryTerms) == 0 {
		t.Fatalf("query terms = %#v", result.QueryTerms)
	}
}

func TestSearchPrioritizesCompactPhraseOverFuzzyRecency(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old useful match", BodyText: "No thanks, I have not got round to setting up a dark room yet.", Date: now.AddDate(-1, 0, 0)},
		{ID: 2, UserID: 1, Subject: "newer weak match", BodyText: "The storage room is ready for pickup.", Date: now.Add(-2 * time.Hour)},
		{ID: 3, UserID: 1, Subject: "newer close word", BodyText: "Wardroom availability changed this week.", Date: now.Add(-time.Hour)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "darkroom", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
	for _, id := range ids {
		if id == 2 || id == 3 {
			t.Fatalf("weak partial/fuzzy match returned for darkroom: ids = %v", ids)
		}
	}
}

func TestSearchDoesNotSplitShortCompactWordIntoFuzzyFragments(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "unrelated recent note", BodyText: "I'll be in front of BRM05 at 11:15, so we can get to the restaurant by 11:30.", Date: now},
		{ID: 2, UserID: 1, Subject: "Shipped: Ilford film", BodyText: "Your Ilford HP5 order is out for delivery.", Date: now.AddDate(0, 0, -30)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "ilford", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchDoesNotSplitCompactWordIntoThreeLetterFragments(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing update", BodyText: "Housing application status", Date: now},
		{ID: 2, UserID: 1, Subject: "Suffix fragment", BodyText: "ing", Date: now.Add(time.Hour)},
		{ID: 3, UserID: 1, Subject: "Split fragment", BodyText: "hous ing", Date: now.Add(2 * time.Hour)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "housing", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestQuotedCompactWordDoesNotSplitOrFuzzyMatch(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Darkroom supplies", BodyText: "Darkroom trays and chemicals", Date: now},
		{ID: 2, UserID: 1, Subject: "Dark room setup", BodyText: "A dark room with a safe light", Date: now},
		{ID: 3, UserID: 1, Subject: "Wardroom", BodyText: "Wardroom schedule", Date: now},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, `"darkroom"`, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchOptionsCanDisableFuzzyMatching(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "darkroom supplies", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "darkrom", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("default fuzzy ids = %v", ids)
	}
	ids, err = svc.SearchWithOptions(ctx, 1, "darkrom", 10, 0, SearchOptions{Behavior: SearchBehavior{Fuzzy: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("fuzzy off ids = %v", ids)
	}
}

func TestSearchOptionsCanExcludeAttachmentText(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "plain note", Date: time.Now(), HasAttachments: true}, []AttachmentDoc{{Filename: "report.pdf", ContentType: "application/pdf", Text: "peculiarterm"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "peculiarterm", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("default attachment ids = %v", ids)
	}
	ids, err = svc.SearchWithOptions(ctx, 1, "peculiarterm", 10, 0, SearchOptions{Behavior: SearchBehavior{AttachmentWeight: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("attachment text off ids = %v", ids)
	}
}

func TestSearchMatchesFromDomain(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "route", FromAddr: "Support <help@mxroute.com>", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "other", FromAddr: "alerts@example.com", Date: time.Now()},
		{ID: 3, UserID: 2, Subject: "route other user", FromAddr: "help@mxroute.com", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{"mxroute.com", "from:mxroute.com", "from:@mxroute.com"} {
		ids, err := svc.Search(ctx, 1, query, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != 1 {
			t.Fatalf("%q ids = %v", query, ids)
		}
	}
}

func TestSearchFromFullEmailRequiresLocalAndDomain(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "target", FromAddr: "Target <target.sender@example.com>", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "same domain", FromAddr: "Other <other@example.com>", Date: time.Now()},
		{ID: 3, UserID: 1, Subject: "same tld", FromAddr: "Dot Com <dot@example.com>", Date: time.Now()},
		{ID: 4, UserID: 1, Subject: "same local", FromAddr: "Target <target.sender@example.net>", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "from:target.sender@example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("full email ids = %v", ids)
	}
}

func TestSearchPrioritizesExactSenderAndSubjectCompound(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{
			ID:       1,
			UserID:   1,
			Subject:  "North Star account update",
			FromAddr: "Updates <news@northstar.example>",
			BodyText: "A direct account message.",
			Date:     time.Now(),
		},
		{
			ID:       2,
			UserID:   1,
			Subject:  "Weekly conditions report",
			FromAddr: "reports@example.com",
			BodyText: strings.Repeat("north ", 30) + "one star was visible",
			Date:     time.Now().Add(time.Minute),
		},
		{
			ID:       3,
			UserID:   2,
			Subject:  "North Star account update",
			FromAddr: "news@northstar.example",
			Date:     time.Now(),
		},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "northstar", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 2 {
		t.Fatalf("ids = %v", ids)
	}
	if ids[0] != 1 {
		t.Fatalf("first id = %d, ids = %v", ids[0], ids)
	}
}

func TestSearchBestBlendsRecencyForBroadTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{
			ID:       1,
			UserID:   1,
			Subject:  "Old busy thread",
			BodyText: strings.Repeat("hello ", 200),
			Date:     now.AddDate(-3, 0, 0),
		},
		{
			ID:       2,
			UserID:   1,
			Subject:  "Quick note",
			BodyText: "hello from today",
			Date:     now.Add(-2 * time.Hour),
		},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "hello", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchNormalRecencyPromotesRecentComparableMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing Help", BodyText: strings.Repeat("Housing Help housing support ", 30), Date: now.AddDate(-9, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Colorado housing update", BodyText: "housing", Date: now.AddDate(0, -5, 0)},
		{ID: 3, UserID: 1, Subject: "Housing policy this month", BodyText: "housing", Date: now.AddDate(0, 0, -20)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "housing", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 2 {
		t.Fatalf("normal recency ids = %v", ids)
	}
}

func TestSearchNormalRecencyBeatsOlderSubjectOnlyAdvantage(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "CedarRoot", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "nick", Date: now.AddDate(0, 0, -26)},
		{ID: 2, UserID: 1, Subject: "Nick update", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: "graham@example.test", BodyText: "nick", Date: now.AddDate(0, -10, 0)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 {
		t.Fatalf("normal recency ids = %v", ids)
	}
}

func TestSearchExplicitDateFilterDisablesRecencyBoost(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msg := store.MessageRecord{ID: 1, UserID: 1, Subject: "Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "nick", Date: time.Now().UTC()}
	if err := svc.IndexMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}

	query := "nick after:2000-01-01"
	withRecency, ok, err := svc.ScoreMessageWithOptions(ctx, 1, msg.ID, query, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil || !ok {
		t.Fatalf("with recency ok=%v err=%v", ok, err)
	}
	withoutRecency, ok, err := svc.ScoreMessageWithOptions(ctx, 1, msg.ID, query, SearchOptions{Behavior: SearchBehavior{RecencyBias: "none"}})
	if err != nil || !ok {
		t.Fatalf("without recency ok=%v err=%v", ok, err)
	}
	if math.Abs(withRecency-withoutRecency) > 0.000001 {
		t.Fatalf("date-filtered score should not include recency boost: with=%v without=%v", withRecency, withoutRecency)
	}
}

func TestSearchStrongRecencyPutsCurrentSenderAboveOlderTripleFieldMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Re: Graham / Nick Introduction", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: strings.Repeat("nick introduction ", 40), Date: now.AddDate(0, -10, 0)},
		{ID: 2, UserID: 1, Subject: "Re: Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "I can talk now. Nick Koncilja", Date: now.Add(-10 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	rawIDs, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "none", SenderBoost: false, SenderBoostSet: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rawIDs) != 2 || rawIDs[0] != 1 {
		t.Fatalf("raw ids = %v", rawIDs)
	}

	boostedIDs, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "strong", SenderBoost: false, SenderBoostSet: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(boostedIDs) != 2 || boostedIDs[0] != 2 {
		t.Fatalf("strong recency ids = %v", boostedIDs)
	}
}

func TestSearchStrongRecencyOverpowersVeryOldDenseMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing Help", BodyText: strings.Repeat("Housing Help housing support ", 300), Date: now.AddDate(-9, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Housing this quarter", BodyText: "housing", Date: now.AddDate(0, -2, 0)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "housing", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "strong"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 {
		t.Fatalf("strong recency ids = %v", ids)
	}
}

func TestSearchBoostsReadSenders(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "note", FromAddr: "known@example.com", BodyText: "status", Date: now},
		{ID: 2, UserID: 1, Subject: "note", FromAddr: "other@example.com", BodyText: "status", Date: now},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.SearchWithOptions(ctx, 1, "status", 10, 0, SearchOptions{
		SenderBoosts: []SenderBoost{{Sender: "known@example.com", Boost: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchDateOperators(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old", BodyText: "invoice", Date: time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "new", BodyText: "invoice", Date: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "edge", BodyText: "statement", Date: time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "invoice after:2025/01/01", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("after ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "invoice before:2025/01/01", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("before ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "year:2025", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("year ids = %v", ids)
	}
}

func TestSearchRelativeDateOperators(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old reservation", BodyText: "yoga", Date: now.Add(-10 * 24 * time.Hour)},
		{ID: 2, UserID: 1, Subject: "new reservation", BodyText: "yoga", Date: now.Add(-2 * 24 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "yoga older_than:7d", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("older_than ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "yoga newer_than:7d", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("newer_than ids = %v", ids)
	}
}

func TestSearchPlainNegatedTermExcludesMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Longmont errands", BodyText: "Downtown longmont lunch plans", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "Longmont spa", BodyText: "Spavia longmont appointment links", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "Spavia receipt", BodyText: "Spavia links without the city", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 4, UserID: 2, Subject: "Longmont other tenant", BodyText: "No spavia here", Date: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "longmont -spavia", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchSenderNameBeatsOlderBodyMentions(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	today := dayBoundary(now, false)
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Old notes", BodyText: strings.Repeat("nick ", 80), FromAddr: "Archive <archive@example.test>", Date: today.AddDate(-5, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Checking In", BodyText: "All good. nbk Nick Koncilja", FromAddr: "\"Nick Koncilja\" <nick@riverrise.com>", Date: today.Add(12 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 {
		t.Fatalf("nick ids = %v", ids)
	}

	hit, ok, err := svc.MatchMessageWithOptions(ctx, 1, 2, "nick after:today", SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected sender message to match nick after:today")
	}
	if len(hit.Terms) == 0 {
		t.Fatalf("expected highlight terms for hit: %+v", hit)
	}

	boostedScore, ok, err := svc.ScoreMessageWithOptions(ctx, 1, 2, "nick", SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil || !ok {
		t.Fatalf("boosted score ok=%v err=%v", ok, err)
	}
	baselineScore, ok, err := svc.ScoreMessageWithOptions(ctx, 1, 2, "nick", SearchOptions{Behavior: SearchBehavior{RecencyBias: "none", SenderBoost: false, SenderBoostSet: true}})
	if err != nil || !ok {
		t.Fatalf("baseline score ok=%v err=%v", ok, err)
	}
	if boostedScore <= baselineScore {
		t.Fatalf("expected recency nudge to raise score: boosted=%v baseline=%v", boostedScore, baselineScore)
	}
}
