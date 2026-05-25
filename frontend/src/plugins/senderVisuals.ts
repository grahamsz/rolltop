// File overview: Sender visual selection helper. It chooses the best available plugin-provided avatar
// or brand image for a thread message.

import type { ThreadMessage } from "../types";
import { bimiSenderVisualURL } from "./bimiBrandIcons";
import { gravatarSenderVisualURL } from "./gravatarSenderIcons";
import type { PluginSet } from "./registry";

/** senderVisualURL chooses the best plugin-provided visual URL for a thread message sender. */
export function senderVisualURL(item: ThreadMessage, brandIcons: Record<string, string>, plugins: PluginSet): string {
  if (item.sender_visual?.url) return item.sender_visual.url;
  return gravatarSenderVisualURL(item, plugins) || bimiSenderVisualURL(item, brandIcons, plugins);
}
