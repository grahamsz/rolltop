// File overview: Tests for conversation grouping, search filtering, snippets, and summary behavior.

package web

import (
	"slices"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestConversationListKeyMergesSameSubjectParticipants(t *testing.T) {
	own := map[string]bool{"me@example.test": true}
	first := []store.MessageRecord{{
		Subject:  "This is a test",
		FromAddr: "Me <me@example.test>",
		ToAddr:   "Graham <graham@example.test>",
	}}
	second := []store.MessageRecord{{
		Subject:  "Re: This is a test",
		FromAddr: "Graham <graham@example.test>",
		ToAddr:   "Me <me@example.test>",
	}}
	other := []store.MessageRecord{{
		Subject:  "This is a test",
		FromAddr: "Other <other@example.test>",
		ToAddr:   "Me <me@example.test>",
	}}
	if conversationListKey(first, own) != conversationListKey(second, own) {
		t.Fatalf("expected sent and received copies to share a conversation key")
	}
	if conversationListKey(first, own) == conversationListKey(other, own) {
		t.Fatalf("expected different participants to stay separate")
	}
}

func TestConversationListKeyKeepsDistinctMessageIDThreadsSeparate(t *testing.T) {
	own := map[string]bool{"me@example.test": true}
	first := []store.MessageRecord{{
		ID:              1,
		MessageIDHeader: "<first@example.test>",
		ThreadKey:       "msgid:first@example.test",
		Subject:         "10,000+ New Arrivals Just Added",
		FromAddr:        `"The RealReal" <email@e.therealreal.com>`,
		ToAddr:          "Me <me@example.test>",
	}}
	second := []store.MessageRecord{{
		ID:              2,
		MessageIDHeader: "<second@example.test>",
		ThreadKey:       "msgid:second@example.test",
		Subject:         "10,000+ New Arrivals Just Added",
		FromAddr:        `"The RealReal" <email@e.therealreal.com>`,
		ToAddr:          "Me <me@example.test>",
	}}
	if conversationListKey(first, own) == conversationListKey(second, own) {
		t.Fatalf("expected marketing messages with distinct Message-IDs to stay separate")
	}
}

func TestStripStarSearchOperators(t *testing.T) {
	query, starred := stripStarSearchOperators(`from:alerts@example.test is:starred "quarterly report"`)
	if query != `from:alerts@example.test "quarterly report"` {
		t.Fatalf("query = %q", query)
	}
	if starred == nil || !*starred {
		t.Fatalf("starred = %v, want true", starred)
	}

	query, starred = stripStarSearchOperators("budget -is:notstarred")
	if query != "budget" {
		t.Fatalf("negated query = %q", query)
	}
	if starred == nil || !*starred {
		t.Fatalf("negated starred = %v, want true", starred)
	}
}

func TestStripInSearchOperators(t *testing.T) {
	query, names := stripInSearchOperators(`report in:Sent from:alerts@example.test in:"Gmail Forward"`)
	if query != `report from:alerts@example.test` {
		t.Fatalf("query = %q", query)
	}
	if got := strings.Join(names, ","); got != "Sent,Gmail Forward" {
		t.Fatalf("names = %q", got)
	}
}

func TestMatchingSearchMailboxIDs(t *testing.T) {
	mailboxes := []store.MailboxSummary{
		{Mailbox: store.Mailbox{ID: 1, Name: "[Gmail]/Sent Mail", Role: "sent"}},
		{Mailbox: store.Mailbox{ID: 2, Name: "Archives.2024"}},
	}
	ids := matchingSearchMailboxIDs(mailboxes, "Sent")
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("sent ids = %v", ids)
	}
	ids = matchingSearchMailboxIDs(mailboxes, "2024")
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("leaf ids = %v", ids)
	}
}

func TestMessageSnippetCleansHTMLPreviewNoise(t *testing.T) {
	raw := `&#847;&zwnj;&#8199;<style>*{box-sizing:border-box}body{margin:0;padding:0}#MessageViewBody a{color:inherit}</style><div>Memorial Day sale <a href="https://example.test/deal">starts now</a></div> https://tracker.example.test/open`
	got := messageSnippet(raw)
	if got != "Memorial Day sale starts now" {
		t.Fatalf("snippet = %q", got)
	}
}

func TestMessageSnippetDecodesStoredISO2022JPPreview(t *testing.T) {
	raw := "\x1b$B4|4V8BDj%]%$%s%H\x1b(B"
	got := messageSnippet(raw)
	if got != "期間限定ポイント" {
		t.Fatalf("snippet = %q", got)
	}
}

func TestMessageSnippetDoesNotRunCSSRuleCleanerAtDisplayTime(t *testing.T) {
	raw := `Intro text {not a css block .promo{color:red} still readable`
	got := messageSnippet(raw)
	if !strings.Contains(got, "Intro text") {
		t.Fatalf("snippet = %q", got)
	}
}

func TestSearchResultSnippetUsesMatchingBodyContext(t *testing.T) {
	msg := store.MessageRecord{
		Subject:  "Quarterly notes",
		FromAddr: "Sender <sender@example.test>",
		BodyText: strings.Join([]string{
			"Opening context that is not the matching part.",
			"The rollout notes mention mirrorstone twice so search results can show useful context.",
			"Closing context that should not hide the matching word.",
		}, " "),
	}
	got := searchResultSnippet("mirrorstone", []string{"mirrorstone"}, msg, "fallback preview")
	if !strings.Contains(got, "mirrorstone") {
		t.Fatalf("snippet does not include match: %q", got)
	}
	if got == "fallback preview" {
		t.Fatalf("snippet fell back instead of using body context")
	}
}

func TestSearchResultDisplayMessageUsesMatchedSeed(t *testing.T) {
	now := time.Now()
	summary := conversationView{
		Message: store.MessageRecord{ID: 2, Subject: "Latest reply", FromAddr: "Me <me@example.test>", Date: now.Add(time.Minute), IsStarred: true},
	}
	seed := store.MessageRecord{ID: 1, Subject: "Checking In", FromAddr: "\"Nick Koncilja\" <nick@riverrise.com>", Date: now}

	display := searchResultDisplayMessage(summary, seed)
	if display.ID != seed.ID {
		t.Fatalf("display id = %d, want matched seed %d", display.ID, seed.ID)
	}
	if !display.IsStarred {
		t.Fatal("expected thread-level starred state to remain visible on search result row")
	}
}

func TestSummarizeConversationDedupesMailboxCopies(t *testing.T) {
	now := time.Now()
	thread := []store.MessageRecord{
		{
			ID:              1,
			MessageIDHeader: "<same@example.test>",
			Subject:         "Sale",
			FromAddr:        "The RealReal <mail@example.test>",
			Date:            now,
			BodyText:        "First copy",
			IsRead:          true,
			IsStarred:       true,
		},
		{
			ID:              2,
			MessageIDHeader: "<same@example.test>",
			Subject:         "Sale",
			FromAddr:        "The RealReal <mail@example.test>",
			Date:            now.Add(time.Second),
			BodyText:        "Duplicate mailbox copy",
			IsRead:          false,
		},
		{
			ID:              3,
			MessageIDHeader: "<reply@example.test>",
			InReplyTo:       "<same@example.test>",
			Subject:         "Re: Sale",
			FromAddr:        "Me <me@example.test>",
			Date:            now.Add(2 * time.Second),
			BodyText:        "Reply",
			IsRead:          true,
		},
	}
	conv := summarizeConversation(thread, map[string]bool{"me@example.test": true})
	if conv.Count != 2 {
		t.Fatalf("count = %d, want 2", conv.Count)
	}
	if !slices.Equal(conv.MessageIDs, []int64{1, 2, 3}) {
		t.Fatalf("message ids = %v, want all physical thread messages", conv.MessageIDs)
	}
	api := apiConversations([]conversationView{conv})
	if len(api) != 1 || !slices.Equal(api[0].MessageIDs, []int64{1, 2, 3}) {
		t.Fatalf("api conversation ids = %+v", api)
	}
	if conv.IsRead {
		t.Fatal("expected unread duplicate copy to keep conversation unread")
	}
	if !conv.Message.IsStarred {
		t.Fatal("expected starred duplicate copy to keep conversation starred")
	}
	if conv.StarredMessageID != 1 {
		t.Fatalf("starred message id = %d, want 1", conv.StarredMessageID)
	}
}
