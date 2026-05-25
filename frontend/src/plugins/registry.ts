// File overview: Frontend plugin registry. It mirrors backend plugin IDs and exposes enabled-plugin
// lookups without letting individual views hard-code feature switches.

export const pluginIDs = {
  bimiBrandIcons: "bimi_brand_icons",
  gravatarSenderIcons: "gravatar_sender_icons",
  remoteImageBlocklist: "remote_image_blocklist",
  trustedImageSources: "trusted_image_sources",
  attachmentPreview: "attachment_preview",
  languageSearch: "language_search",
  oneClickUnsubscribe: "one_click_unsubscribe"
} as const;

/** PluginID is the union of frontend-known plugin identifiers. */
export type PluginID = (typeof pluginIDs)[keyof typeof pluginIDs];
/** PluginSet is the enabled-plugin lookup passed into plugin UI hooks. */
export type PluginSet = ReadonlySet<string>;

/** createPluginSet normalizes an enabled plugin ID array into a readonly lookup set. */
export function createPluginSet(ids: readonly string[] = []): PluginSet {
  return new Set(ids);
}

/** pluginEnabled tests whether a plugin ID is currently active. */
export function pluginEnabled(plugins: PluginSet, id: PluginID): boolean {
  return plugins.has(id);
}
