export const pluginIDs = {
  bimiBrandIcons: "bimi_brand_icons",
  gravatarSenderIcons: "gravatar_sender_icons",
  remoteImageBlocklist: "remote_image_blocklist",
  trustedImageSources: "trusted_image_sources",
  attachmentPreview: "attachment_preview",
  languageSearch: "language_search",
  oneClickUnsubscribe: "one_click_unsubscribe"
} as const;

export type PluginID = (typeof pluginIDs)[keyof typeof pluginIDs];
export type PluginSet = ReadonlySet<string>;

export function createPluginSet(ids: readonly string[] = []): PluginSet {
  return new Set(ids);
}

export function pluginEnabled(plugins: PluginSet, id: PluginID): boolean {
  return plugins.has(id);
}
