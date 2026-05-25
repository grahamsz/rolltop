import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

export function languageSearchSuggestions(plugins: PluginSet): [string, string][] {
  if (!pluginEnabled(plugins, pluginIDs.languageSearch)) return [];
  return [
    ["lang:ja ", "Japanese messages"],
    ["lang:fr ", "French messages"]
  ];
}
