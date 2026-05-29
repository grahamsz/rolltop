// File overview: Sync-layer dispatch for language-search plugin hooks.

package syncer

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
