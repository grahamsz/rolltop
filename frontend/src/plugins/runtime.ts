import type { FrontendPluginDefinition } from "../types";
import { pluginIDs } from "./registry";
import type { ClientSidePGPPlugin } from "../../../plugins/client_side_pgp/frontend/types";

export type RuntimePluginModule = {
  default?: unknown;
};

export type RuntimePlugin = Record<string, unknown>;

export type RuntimePlugins = {
  all: RuntimePlugin[];
  byID: Record<string, RuntimePlugin>;
  clientSidePGP?: ClientSidePGPPlugin;
};

export function emptyRuntimePlugins(): RuntimePlugins {
  return { all: [], byID: {} };
}

export async function loadRuntimePlugins(definitions: readonly FrontendPluginDefinition[] = []): Promise<RuntimePlugins> {
  const plugins = emptyRuntimePlugins();
  syncPluginCSS(definitions);
  await Promise.all(definitions.map(async (definition) => {
    if (!definition.module_url) return;
    const mod = await import(/* @vite-ignore */ definition.module_url) as RuntimePluginModule;
    const plugin = normalizeRuntimePlugin(mod.default);
    if (!plugin) return;
    plugins.all.push(plugin);
    plugins.byID[definition.id] = plugin;
    if (definition.id === pluginIDs.clientSidePGP) plugins.clientSidePGP = plugin as ClientSidePGPPlugin;
  }));
  return plugins;
}

export function getRuntimePlugin<T extends RuntimePlugin>(plugins: RuntimePlugins, id: string): T | undefined {
  return plugins.byID[id] as T | undefined;
}

function normalizeRuntimePlugin(value: unknown): RuntimePlugin | null {
  if (!value || typeof value !== "object") return null;
  return value as RuntimePlugin;
}

function syncPluginCSS(definitions: readonly FrontendPluginDefinition[]) {
  if (typeof document === "undefined") return;
  const prefix = "runtime-plugin-css-";
  const activeIDs = new Set(definitions.filter((definition) => definition.css_url).map((definition) => `${prefix}${definition.id}`));
  document.querySelectorAll<HTMLLinkElement>(`link[id^="${prefix}"]`).forEach((link) => {
    if (!activeIDs.has(link.id)) link.remove();
  });
  definitions.forEach((definition) => {
    if (!definition.css_url) return;
    const id = `${prefix}${definition.id}`;
    const existing = document.getElementById(id) as HTMLLinkElement | null;
    if (existing) {
      if (existing.href !== new URL(definition.css_url, window.location.href).href) {
        existing.href = definition.css_url;
      }
      return;
    }
    const link = document.createElement("link");
    link.id = id;
    link.rel = "stylesheet";
    link.href = definition.css_url;
    document.head.appendChild(link);
  });
}
