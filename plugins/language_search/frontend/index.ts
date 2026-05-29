// File overview: Language-search autocomplete suggestions used when the language plugin is enabled.

import { pluginEnabled, pluginIDs, type PluginSet } from "../../../frontend/src/plugins/registry";

/** languageSearchSuggestions returns search autocomplete entries for language filters when enabled. */
export function languageSearchSuggestions(plugins: PluginSet): [string, string][] {
  if (!pluginEnabled(plugins, pluginIDs.languageSearch)) return [];
  return [
    ["lang:ja ", "Japanese messages"],
    ["lang:fr ", "French messages"]
  ];
}

export default {
  languageSearchSuggestions
};
