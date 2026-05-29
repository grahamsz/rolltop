// File overview: Sender visual selection helper. It chooses the best available plugin-provided avatar
// or brand image for a thread message.

import type { ThreadMessage } from "../types";
import { bimiSenderVisualURL } from "./bimiBrandIcons";
import type { PluginSet } from "./registry";

/** senderVisualURL chooses the best plugin-provided visual URL for a thread message sender. */
export function senderVisualURL(item: ThreadMessage, brandIcons: Record<string, string>, plugins: PluginSet): string {
  const visual = item.sender_visual;
  if (visual?.plugin_id === "contacts") return visual.url;
  const bimi = bimiSenderVisualURL(item, brandIcons, plugins);
  if (bimi) return bimi;
  return visual?.url || "";
}
