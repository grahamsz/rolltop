package web

import (
	"strings"
	"testing"
)

func TestClipTextQuoteUsesStandardReplyMarker(t *testing.T) {
	body := "Thanks, that works for me.\n\nOn Tue, Alice <alice@example.test> wrote:\n> The earlier note\n> with quoted details"
	displayHTML, displayText, hidden := clippedEmailBody("", body, nil)
	if displayHTML != "" {
		t.Fatalf("displayHTML = %q", displayHTML)
	}
	if !hidden {
		t.Fatal("expected quoted text to be hidden")
	}
	if displayText != "Thanks, that works for me." {
		t.Fatalf("displayText = %q", displayText)
	}
}

func TestClipTextQuoteUsesPriorMessageOverlap(t *testing.T) {
	previous := strings.Join([]string{
		"The prior message has enough text to be recognized later.",
		"It spans multiple lines so the overlap is not accidental.",
		"The final line closes the repeated copied section cleanly.",
	}, "\n")
	current := "Fresh reply at the top.\n\n" + previous

	_, displayText, hidden := clippedEmailBody("", current, []string{previous})
	if !hidden {
		t.Fatal("expected repeated prior message to be hidden")
	}
	if displayText != "Fresh reply at the top." {
		t.Fatalf("displayText = %q", displayText)
	}
}

func TestClipHTMLQuoteKeepsRichPrefix(t *testing.T) {
	body := `<div><p>Fresh answer with enough text to keep as rich HTML.</p><blockquote type="cite"><p>Older copied text</p></blockquote></div>`
	displayHTML, _, hidden := clippedEmailBody(body, "Fresh answer with enough text to keep as rich HTML.\n\nOlder copied text", nil)
	if !hidden {
		t.Fatal("expected HTML quote to be hidden")
	}
	if strings.Contains(displayHTML, "Older copied text") {
		t.Fatalf("displayHTML still contains quoted content: %s", displayHTML)
	}
	if !strings.Contains(displayHTML, "Fresh answer") {
		t.Fatalf("displayHTML lost fresh content: %s", displayHTML)
	}
}

func TestClipHTMLQuoteDoesNotRemoveLeadingBlockquoteFormatting(t *testing.T) {
	body := `<blockquote><p>This whole message uses blockquote styling.</p></blockquote>`
	displayHTML, _, hidden := clippedEmailBody(body, "This whole message uses blockquote styling.", nil)
	if hidden {
		t.Fatal("did not expect leading blockquote-only body to be hidden")
	}
	if displayHTML != body {
		t.Fatalf("displayHTML = %q", displayHTML)
	}
}

func TestEmailDocumentRendersPlainTextAsProportionalWrappedText(t *testing.T) {
	doc := emailDocument("", "Hello\n\nThis should wrap like mail.", false)
	if strings.Contains(doc, "<pre>") {
		t.Fatalf("plain text rendered as pre: %s", doc)
	}
	if !strings.Contains(doc, `class="plaintext"`) {
		t.Fatalf("missing plaintext wrapper: %s", doc)
	}
	if !strings.Contains(doc, "white-space:pre-wrap") {
		t.Fatalf("missing whitespace preservation: %s", doc)
	}
}

func TestEmailDocumentAllowsRemoteStylesAndFontsWithImages(t *testing.T) {
	doc := emailDocumentWithBlocklist(`<link rel="stylesheet" href="//cdn.example.test/mail.css"><style>@font-face{src:url(//cdn.example.test/mail.woff2)}</style>`, "", true, nil)
	if !strings.Contains(doc, `style-src 'unsafe-inline' http: https:`) {
		t.Fatalf("remote styles not allowed by CSP: %s", doc)
	}
	if !strings.Contains(doc, `font-src data: http: https:`) {
		t.Fatalf("remote fonts not allowed by CSP: %s", doc)
	}
	if !strings.Contains(doc, `href="https://cdn.example.test/mail.css"`) {
		t.Fatalf("protocol-relative stylesheet URL not normalized: %s", doc)
	}
	if !strings.Contains(doc, `url(https://cdn.example.test/mail.woff2)`) {
		t.Fatalf("protocol-relative font URL not normalized: %s", doc)
	}
}

func TestEmailDocumentRemovesBlockedRemoteImages(t *testing.T) {
	body := `<p>Brand mail</p><img src="https://track.example.test/open.php?id=1"><img src="https://cdn.example.test/logo.png">`
	doc := emailDocumentWithBlocklist(body, "", true, []string{`(?i)/open\.php`})
	if strings.Contains(doc, "open.php") {
		t.Fatalf("blocked tracker image was retained: %s", doc)
	}
	if !strings.Contains(doc, "logo.png") {
		t.Fatalf("legitimate image was removed: %s", doc)
	}
}
