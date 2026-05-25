// File overview: Tests for search indexing, query behavior, tenant isolation, and highlighting.

package search

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailmirror/backend/store"
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

	ids, err := svc.Search(ctx, 1, "alpha", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("user 1 ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "alpha", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("user 2 alpha ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "beta", SortRecent, 10, 0)
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
	ids, err := svc.Search(ctx, 1, "alpha", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchRecentStillAppliesGmailOperators(t *testing.T) {
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
	ids, err := svc.Search(ctx, 1, "has:attachment", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 1 {
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

	ids, err := svc.Search(ctx, 1, "alpha is:starred", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "alpha is:notstarred", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("not starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 2, "alpha is:starred", SortRecent, 10, 0)
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

	ids, err := svc.Search(ctx, 1, "lang:fr", SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("fr ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "lang:fr facture", SortRecent, 10, 0)
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
	ids, err := svc.Search(ctx, 1, "riverrise", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "riverrse", SortBest, 10, 0)
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
	ids, err := svc.Search(ctx, 1, "dark room", SortBest, 10, 0)
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
	hits, err := svc.SearchHitsWithOptions(ctx, 1, "darkk room", SortBest, 10, 0, SearchOptions{})
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
	ids, err := svc.Search(ctx, 1, "darkroom", SortBest, 10, 0)
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
	ids, err := svc.Search(ctx, 1, "ilford", SortBest, 10, 0)
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
	ids, err := svc.Search(ctx, 1, "housing", SortBest, 10, 0)
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
	ids, err := svc.Search(ctx, 1, `"darkroom"`, SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
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
		ids, err := svc.Search(ctx, 1, query, SortBest, 10, 0)
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

	ids, err := svc.Search(ctx, 1, "from:target.sender@example.com", SortBest, 10, 0)
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

	ids, err := svc.Search(ctx, 1, "northstar", SortBest, 10, 0)
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

	ids, err := svc.Search(ctx, 1, "hello", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 1 {
		t.Fatalf("ids = %v", ids)
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
	ids, err := svc.SearchWithOptions(ctx, 1, "status", SortBest, 10, 0, SearchOptions{
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
	ids, err := svc.Search(ctx, 1, "invoice after:2025/01/01", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("after ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "invoice before:2025/01/01", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("before ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "year:2025", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("year ids = %v", ids)
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

	ids, err := svc.Search(ctx, 1, "longmont -spavia", SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}
