// File overview: BIMI brand-icon frontend helper. It batches domains from a thread and asks the
// backend for cached or resolvable brand assets used only in message view.

import { api } from "../../api";
import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

/** senderDomain extracts a normalized sender domain from a name/address string. */
export function senderDomain(value: string): string {
  const match = String(value || "").match(/@([^>\s,;]+)/);
  if (!match) return "";
  return match[1]
    .toLowerCase()
    .replace(/[)"'.,;:]+$/g, "")
    .split("/")
    .shift() || "";
}

/** brandDomainKeyForThread collects BIMI domains from a thread when the plugin is enabled. */
export function brandDomainKeyForThread(thread: ThreadMessage[], plugins: PluginSet): string {
  if (!pluginEnabled(plugins, pluginIDs.bimiBrandIcons)) return "";
  return thread
    .map((item) => senderDomain(item.sender_email || item.message.from_addr))
    .filter(Boolean)
    .filter((domain, index, domains) => domains.indexOf(domain) === index)
    .join(",");
}

export async function loadBrandIconsForDomains(domainKey: string): Promise<Record<string, string>> {
  const domains = domainKey.split(",").filter(Boolean);
  if (domains.length === 0) return {};
  const data = await api.brandIcons(domains);
  return data.icons || {};
}

/** bimiSenderVisualURL returns the cached BIMI image URL for a thread message sender domain. */
export function bimiSenderVisualURL(
  item: ThreadMessage,
  brandIcons: Record<string, string>,
  plugins: PluginSet
): string {
  if (!pluginEnabled(plugins, pluginIDs.bimiBrandIcons)) return "";
  const visual = item.sender_visual;
  if (visual?.plugin_id === pluginIDs.bimiBrandIcons) return visual.url;
  const domain = senderDomain(item.sender_email || item.message.from_addr);
  return domain ? brandIcons[domain] || "" : "";
}
