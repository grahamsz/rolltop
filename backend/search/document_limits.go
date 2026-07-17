package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bleve analysis is synchronous and cannot be canceled. Bound every value that
// can originate in a message body so one pathological MIME part cannot pin the
// tenant writer indefinitely. These limits preserve substantially more text
// than the local body preview while keeping a five-message maintenance commit
// to a predictable memory and CPU budget.
const (
	maxIndexedBodyBytes        = 1024 * 1024
	maxIndexedAttachmentsBytes = 1024 * 1024
	maxIndexedHeaderBytes      = 64 * 1024
	maxIndexedNamesBytes       = 128 * 1024
	maxIndexedTokenRunBytes    = 8 * 1024
	maxIndexedLanguageBytes    = 32
)

func boundedIndexText(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if len(value) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		value = value[:cut]
	}
	value = strings.ToValidUTF8(value, "")
	value = splitOversizedIndexTokens(value)
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func splitOversizedIndexTokens(value string) string {
	runBytes := 0
	lastWritten := 0
	var result strings.Builder
	changed := false
	for offset, r := range value {
		if unicode.IsSpace(r) {
			runBytes = 0
			continue
		}
		runeBytes := utf8.RuneLen(r)
		if runBytes+runeBytes > maxIndexedTokenRunBytes {
			if !changed {
				result.Grow(len(value) + len(value)/maxIndexedTokenRunBytes)
			}
			result.WriteString(value[lastWritten:offset])
			result.WriteByte(' ')
			lastWritten = offset
			runBytes = 0
			changed = true
		}
		runBytes += runeBytes
	}
	if !changed {
		return value
	}
	result.WriteString(value[lastWritten:])
	return result.String()
}

func boundedIndexJoin(values []string, limit int) string {
	if limit <= 0 || len(values) == 0 {
		return ""
	}
	capacity := 0
	for _, value := range values {
		if value == "" {
			continue
		}
		if capacity > 0 {
			capacity++
		}
		capacity += len(value)
		if capacity >= limit {
			capacity = limit
			break
		}
	}
	var result strings.Builder
	result.Grow(capacity)
	for _, value := range values {
		if value == "" {
			continue
		}
		separator := 0
		if result.Len() > 0 {
			separator = 1
		}
		remaining := limit - result.Len() - separator
		if remaining <= 0 {
			break
		}
		if separator > 0 {
			result.WriteByte(' ')
		}
		result.WriteString(boundedIndexText(value, remaining))
		if len(value) > remaining {
			break
		}
	}
	return result.String()
}
