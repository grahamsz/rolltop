// File overview: Gravatar sender-icon helper for plugin-provided sender visuals.

import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

/** gravatarSenderVisualURL returns a Gravatar image URL for a thread sender when the plugin is enabled. */
export function gravatarSenderVisualURL(item: ThreadMessage, plugins: PluginSet): string {
  const visual = item.sender_visual;
  if (visual?.plugin_id !== pluginIDs.gravatarSenderIcons) return "";
  if (!pluginEnabled(plugins, pluginIDs.gravatarSenderIcons)) return "";
  return visual.url;
}
