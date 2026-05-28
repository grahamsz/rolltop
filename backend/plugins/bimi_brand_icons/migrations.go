// File overview: BIMI brand icon plugin storage, lookup, and asset URL helpers. migration declarations. Plugin schema migration declarations and helper persistence.

package bimi_brand_icons

import "rolltop/backend/plugins"

// Migrations returns schema changes for the BIMI icon cache.
func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID: plugins.BIMIBrandIcons,
		ID:       "001_create_brand_icon_cache",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_bimi_brand_icons (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				domain TEXT NOT NULL,
				logo_url TEXT NOT NULL DEFAULT '',
				svg TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				fetched_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL DEFAULT 0,
				UNIQUE(user_id, domain)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_bimi_brand_icons_user_domain ON plugin_bimi_brand_icons(user_id, domain)`,
		},
	}}
}

// AssetURL builds the local URL where the frontend can request a cached BIMI asset by domain.
func AssetURL(domain string) string {
	return "/plugins/bimi_brand_icons/brand-icons/" + NormalizeDomain(domain) + ".svg"
}
