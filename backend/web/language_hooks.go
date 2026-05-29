// File overview: Web-layer dispatch for language-search plugin hooks.

package web

import "rolltop/backend/plugins"

func detectLanguageCode(subject, body string) string {
	for _, hook := range plugins.Hooks(plugins.LanguageSearch) {
		detector, ok := hook.(plugins.LanguageSearchHook)
		if ok {
			return detector.DetectLanguage(subject, body)
		}
	}
	return ""
}
