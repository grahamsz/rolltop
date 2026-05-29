package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/language_search/detector"
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
