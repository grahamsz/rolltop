// File overview: Search-layer dispatch for language-search plugin hooks.

package search

import (
	"strings"

	"rolltop/backend/plugins"
)

func normalizeLanguageCode(code string) string {
	for _, hook := range plugins.Hooks(plugins.LanguageSearch) {
		normalizer, ok := hook.(plugins.LanguageSearchHook)
		if ok {
			return boundedLanguageCode(normalizer.NormalizeLanguageCode(code))
		}
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return boundedLanguageCode(code)
}

func boundedLanguageCode(code string) string {
	return boundedIndexText(code, maxIndexedLanguageBytes)
}
