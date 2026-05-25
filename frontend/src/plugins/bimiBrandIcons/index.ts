import { api } from "../../api";
import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

export function senderDomain(value: string): string {
  const match = String(value || "").match(/@([^>\s,;]+)/);
  if (!match) return "";
  return match[1]
    .toLowerCase()
    .replace(/[)"'.,;:]+$/g, "")
    .split("/")
    .shift() || "";
}

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
