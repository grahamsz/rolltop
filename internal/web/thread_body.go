package web

import (
	"regexp"
	"strings"
	"unicode"
)

var remoteImageRE = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*['"]?\s*https?://`)

type normalizedLine struct {
	value    string
	original int
}

func clippedEmailBody(bodyHTML, bodyText string, previousBodies []string) (string, string, bool) {
	displayText, textHidden := clipTextQuote(bodyText, previousBodies)
	displayHTML, htmlHidden := clipHTMLQuote(bodyHTML)
	if strings.TrimSpace(bodyHTML) == "" {
		return "", displayText, textHidden
	}
	if htmlHidden {
		return displayHTML, displayText, true
	}
	if textHidden {
		return "", displayText, true
	}
	return bodyHTML, bodyText, false
}

func hasRemoteImages(bodyHTML string) bool {
	return remoteImageRE.MatchString(bodyHTML)
}

func clipHTMLQuote(bodyHTML string) (string, bool) {
	bodyHTML = strings.ReplaceAll(bodyHTML, "\x00", "")
	if strings.TrimSpace(bodyHTML) == "" {
		return bodyHTML, false
	}
	lower := strings.ToLower(bodyHTML)
	markers := []string{
		`class="gmail_quote`,
		`class='gmail_quote`,
		`class="gmail_attr`,
		`class='gmail_attr`,
		`class="yahoo_quoted`,
		`class='yahoo_quoted`,
		`id="yahoo_quoted`,
		`id='yahoo_quoted`,
		`class="moz-cite-prefix`,
		`class='moz-cite-prefix`,
		`type="cite"`,
		`type='cite'`,
		`<blockquote`,
		`-----original message-----`,
		`begin forwarded message:`,
	}
	best := -1
	for _, marker := range markers {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		cut := idx
		if strings.Contains(marker, "class=") || strings.Contains(marker, "id=") || strings.Contains(marker, "type=") {
			if tagStart := strings.LastIndex(lower[:idx], "<"); tagStart >= 0 {
				cut = tagStart
			}
		}
		if !hasSubstantialPrefix(bodyHTML[:cut]) {
			continue
		}
		if best < 0 || cut < best {
			best = cut
		}
	}
	if best < 0 {
		return bodyHTML, false
	}
	return strings.TrimSpace(bodyHTML[:best]), true
}

func clipTextQuote(bodyText string, previousBodies []string) (string, bool) {
	lines := splitBodyLines(bodyText)
	if len(lines) == 0 {
		return bodyText, false
	}
	cut := standardQuoteCutLine(lines)
	if adaptive := adaptiveQuoteCutLine(lines, previousBodies); adaptive >= 0 && (cut < 0 || adaptive < cut) {
		cut = adaptive
	}
	if cut < 0 {
		return bodyText, false
	}
	return strings.TrimSpace(strings.Join(lines[:cut], "\n")), true
}

func standardQuoteCutLine(lines []string) int {
	seenContent := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !seenContent {
			if !strings.HasPrefix(trimmed, ">") {
				seenContent = true
			}
			continue
		}
		switch {
		case strings.Contains(lower, " wrote:") && (strings.HasPrefix(lower, "on ") || strings.Contains(lower, "@")):
			return i
		case strings.Contains(lower, "-----original message-----"):
			return i
		case strings.HasPrefix(lower, "begin forwarded message:"):
			return i
		case strings.HasPrefix(lower, "from:") && headerBlockFollows(lines, i):
			return i
		case strings.HasPrefix(trimmed, ">") && quoteBlockFollows(lines, i):
			return i
		case isDividerLine(trimmed) && i+1 < len(lines) && headerBlockFollows(lines, i+1):
			return i
		}
	}
	return -1
}

func adaptiveQuoteCutLine(lines []string, previousBodies []string) int {
	current := normalizedBodyLines(strings.Join(lines, "\n"))
	if len(current) < 2 || len(previousBodies) == 0 {
		return -1
	}
	for curIdx := 1; curIdx < len(current); curIdx++ {
		if !hasVisiblePrefixLines(lines, current[curIdx].original) {
			continue
		}
		for _, previousBody := range previousBodies {
			previous := normalizedBodyLines(previousBody)
			for prevIdx := 0; prevIdx < len(previous); prevIdx++ {
				matches, chars := consecutiveLineMatches(current[curIdx:], previous[prevIdx:])
				if (matches >= 3 && chars >= 80) || (matches >= 1 && chars >= 220) {
					return current[curIdx].original
				}
			}
		}
	}
	return -1
}

func consecutiveLineMatches(current, previous []normalizedLine) (int, int) {
	matches, chars := 0, 0
	for matches < len(current) && matches < len(previous) {
		if current[matches].value != previous[matches].value {
			break
		}
		matches++
		chars += len(current[matches-1].value)
	}
	return matches, chars
}

func normalizedBodyLines(body string) []normalizedLine {
	lines := splitBodyLines(body)
	out := make([]normalizedLine, 0, len(lines))
	for i, line := range lines {
		value := normalizeBodyLine(line)
		if value == "" {
			continue
		}
		out = append(out, normalizedLine{value: value, original: i})
	}
	return out
}

func normalizeBodyLine(line string) string {
	line = strings.TrimSpace(line)
	for strings.HasPrefix(line, ">") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
	}
	if line == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(line) {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(b.String())
}

func splitBodyLines(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func quoteBlockFollows(lines []string, start int) bool {
	count, chars := 0, 0
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ">") {
			break
		}
		count++
		chars += len(line)
	}
	return count >= 2 || chars >= 80
}

func headerBlockFollows(lines []string, start int) bool {
	found := 0
	for i := start; i < len(lines) && i < start+8; i++ {
		lower := strings.ToLower(strings.TrimSpace(lines[i]))
		switch {
		case strings.HasPrefix(lower, "from:"):
			found++
		case strings.HasPrefix(lower, "sent:"):
			found++
		case strings.HasPrefix(lower, "date:"):
			found++
		case strings.HasPrefix(lower, "to:"):
			found++
		case strings.HasPrefix(lower, "cc:"):
			found++
		case strings.HasPrefix(lower, "subject:"):
			found++
		}
	}
	return found >= 2
}

func isDividerLine(line string) bool {
	if len(line) < 8 {
		return false
	}
	for _, r := range line {
		if r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func hasSubstantialPrefix(htmlFragment string) bool {
	return len([]rune(stripHTMLTags(htmlFragment))) >= 20
}

func stripHTMLTags(value string) string {
	var b strings.Builder
	inTag := false
	lastSpace := false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case inTag:
			continue
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func hasVisiblePrefixLines(lines []string, before int) bool {
	chars := 0
	for i := 0; i < before && i < len(lines); i++ {
		chars += len(strings.TrimSpace(lines[i]))
		if chars >= 20 {
			return true
		}
	}
	return false
}
