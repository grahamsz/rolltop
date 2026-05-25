// File overview: Snippet cleaning, search term extraction, and context selection for list/search previews.

package web

import (
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/store"
)

const snippetPreviewBytes = 280
const searchSnippetScanBytes = 8192

var (
	htmlNoiseBlockRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<head\b[^>]*>.*?</head>|<title\b[^>]*>.*?</title>|<svg\b[^>]*>.*?</svg>|<math\b[^>]*>.*?</math>|<!--.*?-->`)
	markdownLinkRE   = regexp.MustCompile(`\[([^\]\n]{1,180})\]\(https?://[^)\s]+[^\)]*\)`)
	bareURLRE        = regexp.MustCompile(`https?://\S+`)
)

func messageSnippet(values ...string) string {
	for _, value := range values {
		if text := cleanSnippetText(value, snippetPreviewBytes); text != "" {
			return text
		}
	}
	return ""
}

func threadSnippet(values ...string) string {
	return messageSnippet(values...)
}

func searchResultSnippet(query string, matchTerms []string, msg store.MessageRecord, fallback string) string {
	terms := append([]string{}, matchTerms...)
	terms = mergeSnippetTerms(terms, searchSnippetTerms(query))
	if len(terms) == 0 {
		return fallback
	}
	for _, value := range []string{msg.BodyText, msg.BodyHTML, msg.Subject, msg.FromAddr, msg.ToAddr, msg.CCAddr} {
		text := cleanSnippetText(value, searchSnippetScanBytes)
		if text == "" {
			continue
		}
		if snippet := snippetAroundSearchTerm(text, terms); snippet != "" {
			return snippet
		}
	}
	return fallback
}

func mergeSnippetTerms(values ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, terms := range values {
		for _, term := range terms {
			term = strings.TrimSpace(strings.ToLower(term))
			if term == "" || seen[term] {
				continue
			}
			seen[term] = true
			out = append(out, term)
			if len(out) >= 16 {
				return out
			}
		}
	}
	return out
}

func searchSnippetTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(value string) {
		value = strings.TrimSpace(strings.Trim(value, `"'`+"`"+`~*()[]{}<>.,;:`))
		if value == "" {
			return
		}
		lower := strings.ToLower(value)
		switch lower {
		case "and", "or", "not":
			return
		}
		if seen[lower] {
			return
		}
		seen[lower] = true
		terms = append(terms, value)
	}
	for _, token := range searchQueryTokens(query) {
		field, value, hasField := strings.Cut(token, ":")
		if hasField {
			switch strings.ToLower(strings.TrimSpace(field)) {
			case "after", "before", "newer", "older", "year", "in", "has", "is", "lang":
				continue
			default:
				token = value
			}
		}
		add(token)
		for _, word := range strings.FieldsFunc(token, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if utf8.RuneCountInString(word) >= 3 {
				add(word)
			}
		}
	}
	sort.SliceStable(terms, func(i, j int) bool {
		return len([]rune(terms[i])) > len([]rune(terms[j]))
	})
	if len(terms) > 16 {
		terms = terms[:16]
	}
	return terms
}

func searchQueryTokens(query string) []string {
	var tokens []string
	for i := 0; i < len(query); {
		for i < len(query) {
			r, size := utf8.DecodeRuneInString(query[i:])
			if !unicode.IsSpace(r) {
				break
			}
			i += size
		}
		if i >= len(query) {
			break
		}
		var b strings.Builder
		var quote rune
		for i < len(query) {
			r, size := utf8.DecodeRuneInString(query[i:])
			if quote != 0 {
				if r == quote {
					quote = 0
					i += size
					continue
				}
				b.WriteRune(r)
				i += size
				continue
			}
			if r == '"' || r == '\'' {
				quote = r
				i += size
				continue
			}
			if unicode.IsSpace(r) {
				break
			}
			b.WriteRune(r)
			i += size
		}
		if token := strings.TrimSpace(b.String()); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func snippetAroundSearchTerm(text string, terms []string) string {
	lowerText := strings.ToLower(text)
	bestStart := -1
	bestEnd := -1
	for _, term := range terms {
		lowerTerm := strings.ToLower(term)
		if lowerTerm == "" {
			continue
		}
		if idx := strings.Index(lowerText, lowerTerm); idx >= 0 && (bestStart < 0 || idx < bestStart) {
			bestStart = idx
			bestEnd = idx + len(lowerTerm)
		}
	}
	if bestStart < 0 {
		return ""
	}
	start := bestStart - 110
	if start < 0 {
		start = 0
	}
	end := bestEnd + 170
	if end > len(text) {
		end = len(text)
	}
	start = safeSnippetBoundaryStart(text, start)
	end = safeSnippetBoundaryEnd(text, end)
	snippet := strings.TrimSpace(text[start:end])
	if snippet == "" {
		return ""
	}
	if start > 0 {
		snippet = "... " + snippet
	}
	if end < len(text) {
		snippet += " ..."
	}
	return trimSnippetBytes(snippet, snippetPreviewBytes)
}

func safeSnippetBoundaryStart(text string, idx int) int {
	if idx <= 0 {
		return 0
	}
	if idx >= len(text) {
		idx = len(text) - 1
	}
	for idx > 0 && !utf8.RuneStart(text[idx]) {
		idx--
	}
	return idx
}

func safeSnippetBoundaryEnd(text string, idx int) int {
	if idx >= len(text) {
		return len(text)
	}
	for idx < len(text) && !utf8.RuneStart(text[idx]) {
		idx++
	}
	return idx
}

func cleanSnippetText(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if value == "" {
		return ""
	}
	value = mailparse.DecodeTextBytes([]byte(value), "")
	value = html.UnescapeString(value)
	value = htmlNoiseBlockRE.ReplaceAllString(value, " ")
	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		value = stripHTMLTags(value)
	}
	value = html.UnescapeString(value)
	value = markdownLinkRE.ReplaceAllString(value, "$1")
	value = bareURLRE.ReplaceAllString(value, "")
	value = strings.Join(strings.Fields(removeInvisiblePreviewRunes(value)), " ")
	value = trimPreviewJunk(value)
	return trimSnippetBytes(value, limit)
}

func removeInvisiblePreviewRunes(value string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		switch {
		case r == '\u034f' || unicode.Is(unicode.Cf, r):
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
	return b.String()
}

func removeCSSRules(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		openRel := strings.IndexByte(value[i:], '{')
		if openRel < 0 {
			b.WriteString(value[i:])
			break
		}
		open := i + openRel
		closeRel := strings.IndexByte(value[open:], '}')
		if closeRel < 0 {
			b.WriteString(value[i:])
			break
		}
		close := open + closeRel
		start := cssSelectorStart(value, open)
		selector := strings.TrimSpace(value[start:open])
		body := strings.TrimSpace(value[open+1 : close])
		if looksLikeCSSRule(selector, body) {
			b.WriteString(value[i:start])
			b.WriteByte(' ')
			i = close + 1
			continue
		}
		b.WriteString(value[i : open+1])
		i = open + 1
	}
	return b.String()
}

func cssSelectorStart(value string, open int) int {
	start := open
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(value[:start])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '}' || r == ';' || r == '\n' || r == '\r' {
			break
		}
		start -= size
	}
	for start < open {
		r, size := utf8.DecodeRuneInString(value[start:open])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	return start
}

func looksLikeCSSRule(selector, body string) bool {
	if selector == "" || body == "" || len(body) > 2000 {
		return false
	}
	lowerSelector := strings.ToLower(selector)
	lowerBody := strings.ToLower(body)
	if !strings.Contains(lowerBody, ":") {
		return false
	}
	for _, token := range []string{"margin", "padding", "color", "font", "display", "width", "height", "box-sizing", "line-height", "text-decoration", "background", "border"} {
		if strings.Contains(lowerBody, token+":") || strings.Contains(lowerBody, token+"-") {
			return true
		}
	}
	return strings.ContainsAny(lowerSelector, "#.*>[],:") ||
		strings.Contains(lowerSelector, "body") ||
		strings.Contains(lowerSelector, "table") ||
		strings.Contains(lowerSelector, "div") ||
		strings.Contains(lowerSelector, "span") ||
		strings.Contains(lowerSelector, "img")
}

func trimPreviewJunk(value string) string {
	value = strings.TrimSpace(value)
	for {
		next := strings.TrimLeft(value, " -_.,;:|")
		if next == value {
			return value
		}
		value = strings.TrimSpace(next)
	}
}

func trimSnippetBytes(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	suffix := " ..."
	cut := limit - len(suffix)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return strings.TrimSpace(value[:cut]) + suffix
}
