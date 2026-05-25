import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

export function gravatarSenderVisualURL(item: ThreadMessage, plugins: PluginSet): string {
  const visual = item.sender_visual;
  if (visual?.plugin_id !== pluginIDs.gravatarSenderIcons) return "";
  if (!pluginEnabled(plugins, pluginIDs.gravatarSenderIcons)) return "";
  return visual.url;
}
