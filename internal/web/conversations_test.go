package web

import (
	"strings"
	"testing"
	"time"

	"mailmirror/internal/store"
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
