package store

import (
	"strings"

	"rolltop/backend/plugins"
)

type pluginCatalog struct {
	definitions []plugins.Definition
	migrations  []plugins.Migration
}

func defaultPluginCatalog() pluginCatalog {
	return pluginCatalog{
		definitions: append([]plugins.Definition(nil), plugins.All()...),
		migrations:  append([]plugins.Migration(nil), plugins.Migrations("")...),
	}
}

func pluginCatalogFromManifests(manifests []plugins.Manifest) pluginCatalog {
	definitions := mergePluginDefinitions(plugins.All(), plugins.DefinitionsFromManifests(manifests))
	migrations := mergePluginMigrations(plugins.Migrations(""), plugins.MigrationsFromManifests(manifests, ""))
	return pluginCatalog{definitions: definitions, migrations: migrations}
}

func mergePluginDefinitions(first, second []plugins.Definition) []plugins.Definition {
	out := make([]plugins.Definition, 0, len(first)+len(second))
	seen := map[string]bool{}
	for _, def := range append(first, second...) {
		def.ID = strings.TrimSpace(def.ID)
		if def.ID == "" || seen[def.ID] {
			continue
		}
		seen[def.ID] = true
		out = append(out, def)
	}
	return out
}

func mergePluginMigrations(first, second []plugins.Migration) []plugins.Migration {
	out := make([]plugins.Migration, 0, len(first)+len(second))
	seen := map[string]bool{}
	for _, migration := range append(first, second...) {
		migration.PluginID = strings.TrimSpace(migration.PluginID)
		migration.Scope = strings.TrimSpace(migration.Scope)
		migration.ID = strings.TrimSpace(migration.ID)
		if migration.PluginID == "" || migration.ID == "" {
			continue
		}
		key := migration.PluginID + "\x00" + migration.Scope + "\x00" + migration.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, migration)
	}
	return out
}
