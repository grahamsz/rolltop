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
			return normalizer.NormalizeLanguageCode(code)
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
	return code
}
