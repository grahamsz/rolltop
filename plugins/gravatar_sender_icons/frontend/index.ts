// File overview: Gravatar sender-icon helper for plugin-provided sender visuals.

import type { ThreadMessage } from "../../../frontend/src/types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../../../frontend/src/plugins/registry";

/** gravatarSenderVisualURL returns a Gravatar image URL for a thread sender when the plugin is enabled. */
export function gravatarSenderVisualURL(item: ThreadMessage, plugins: PluginSet): string {
  const visual = item.sender_visual;
  if (visual?.plugin_id !== pluginIDs.gravatarSenderIcons) return "";
  if (!pluginEnabled(plugins, pluginIDs.gravatarSenderIcons)) return "";
  return visual.url;
}

export default {
  gravatarSenderVisualURL
};
