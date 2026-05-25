import type { ThreadMessage } from "../types";
import { bimiSenderVisualURL } from "./bimiBrandIcons";
import { gravatarSenderVisualURL } from "./gravatarSenderIcons";
import type { PluginSet } from "./registry";

export function senderVisualURL(item: ThreadMessage, brandIcons: Record<string, string>, plugins: PluginSet): string {
  if (item.sender_visual?.url) return item.sender_visual.url;
  return gravatarSenderVisualURL(item, plugins) || bimiSenderVisualURL(item, brandIcons, plugins);
}
