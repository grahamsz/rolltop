package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/language_search/detector"
	"rolltop/plugins/language_search/schema"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.LanguageSearch}
}

type languageSearchHook struct{}

func (languageSearchHook) DetectLanguage(subject, body string) string {
	return detector.DetectCode(subject, body)
}

func (languageSearchHook) NormalizeLanguageCode(code string) string {
	return detector.NormalizeCode(code)
}

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
