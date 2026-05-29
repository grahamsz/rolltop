package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/language_search/schema"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.LanguageSearch,
		Name:             "Language search",
		Description:      "Detects message language during indexing and enables lang: search filters.",
		EnabledByDefault: true,
		Heavy:            true,
	}, schema.Migrations()...)
	plugins.RegisterHooks(plugins.LanguageSearch, languageSearchHook{})
}
