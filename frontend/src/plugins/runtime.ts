import type { ReactNode } from "react";
import type { AddToast, DatePrefs, LocationState, Navigate } from "../appTypes";
import type { SettingsSectionID } from "../features/settings/SettingsUI";
import type { FrontendPluginDefinition, Mailbox, Message, MessageAnnotation, User } from "../types";

export type RuntimePluginModule = {
  default?: unknown;
};

export type RuntimePlugin = Record<string, unknown>;

export type AccountSettingsRouteContext = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  location: LocationState;
  navigate: Navigate;
  addToast: AddToast;
};

export type AccountSettingsRoute = {
  /** path is the canonical URL used by settings navigation. */
  path: string;
  /** aliases preserve old deep links while callers redirect or render the canonical page. */
  aliases?: readonly string[];
  title: string;
  label: string;
  description: string;
  icon: string;
  section?: SettingsSectionID;
  render: (context: AccountSettingsRouteContext) => ReactNode;
};

export type AccountSettingsRuntimePlugin = RuntimePlugin & {
  accountSettingsRoutes?: readonly AccountSettingsRoute[];
};

/** RuntimeMessageDetailsContext is the stable, read-only input for plugin rows
 * rendered inside a message's expanded header details. */
export type RuntimeMessageDetailsContext = {
  message: Message;
  annotations: readonly MessageAnnotation[];
  datePrefs: DatePrefs;
};

/** RuntimeMessageDetailsPlugin contributes dt/dd rows to the message details list. */
export type RuntimeMessageDetailsPlugin = RuntimePlugin & {
  renderMessageDetails?: (context: RuntimeMessageDetailsContext) => ReactNode;
};

export type RuntimePlugins = {
  all: RuntimePlugin[];
  byID: Record<string, RuntimePlugin>;
  status: "loading" | "ready";
  errors: RuntimePluginLoadError[];
};

export type RuntimePluginLoadError = {
  id: string;
  name: string;
  message: string;
};

export function emptyRuntimePlugins(): RuntimePlugins {
  return { all: [], byID: {}, status: "loading", errors: [] };
}

export async function loadRuntimePlugins(definitions: readonly FrontendPluginDefinition[] = []): Promise<RuntimePlugins> {
  syncPluginCSS(definitions);
  const results = await Promise.all(definitions.map(async (definition) => {
    if (!definition.module_url) {
      return { definition, plugin: null, error: "Plugin module URL is missing." };
    }
    try {
      const mod = await import(/* @vite-ignore */ definition.module_url) as RuntimePluginModule;
      const plugin = normalizeRuntimePlugin(mod.default);
      return plugin
        ? { definition, plugin, error: "" }
        : { definition, plugin: null, error: "Plugin module did not export a runtime plugin." };
    } catch (error) {
      return { definition, plugin: null, error: runtimePluginErrorMessage(error) };
    }
  }));

  // Promise.all preserves input order even when imports finish out of order. Build
  // both lookups afterward so hook rendering and route precedence stay stable.
  const plugins: RuntimePlugins = { all: [], byID: {}, status: "ready", errors: [] };
  results.forEach(({ definition, plugin, error }) => {
    if (plugin) {
      plugins.all.push(plugin);
      plugins.byID[definition.id] = plugin;
      return;
    }
    plugins.errors.push({
      id: definition.id,
      name: definition.name || definition.id,
      message: error || "Plugin failed to load."
    });
  });
  return plugins;
}

export function getRuntimePlugin<T extends RuntimePlugin>(plugins: RuntimePlugins, id: string): T | undefined {
  return plugins.byID[id] as T | undefined;
}

/** accountSettingsRoutes returns normalized plugin pages in deterministic plugin/registration order. */
export function accountSettingsRoutes(plugins: RuntimePlugins | readonly RuntimePlugin[]): AccountSettingsRoute[] {
  const list: readonly RuntimePlugin[] = Array.isArray(plugins) ? plugins : (plugins as RuntimePlugins).all;
  return (list as readonly AccountSettingsRuntimePlugin[]).flatMap((plugin) => {
    const routes = Array.isArray(plugin.accountSettingsRoutes) ? plugin.accountSettingsRoutes : [];
    return routes.flatMap((route) => {
      const normalized = normalizeAccountSettingsRoute(route);
      return normalized ? [normalized] : [];
    });
  });
}

/** matchAccountSettingsRoute matches canonical settings paths and compatibility aliases. */
export function matchAccountSettingsRoute(
  plugins: RuntimePlugins | readonly RuntimePlugin[],
  path: string
): AccountSettingsRoute | null {
  const candidate = normalizedSettingsPath(path);
  return accountSettingsRoutes(plugins).find((route) =>
    normalizedSettingsPath(route.path) === candidate
      || (route.aliases || []).some((alias) => normalizedSettingsPath(alias) === candidate)
  ) || null;
}

function normalizeRuntimePlugin(value: unknown): RuntimePlugin | null {
  if (!value || typeof value !== "object") return null;
  return value as RuntimePlugin;
}

function normalizeAccountSettingsRoute(value: unknown): AccountSettingsRoute | null {
  if (!value || typeof value !== "object") return null;
  const route = value as Partial<AccountSettingsRoute>;
  if (typeof route.path !== "string" || !route.path.trim() || typeof route.render !== "function") return null;
  const path = normalizedSettingsPath(route.path);
  const fallbackLabel = settingsLabelFromPath(path);
  const rawAliases = Array.isArray(route.aliases) ? route.aliases : [];
  const aliases = Array.from(new Set(rawAliases
    .filter((alias): alias is string => typeof alias === "string" && Boolean(alias.trim()))
    .map(normalizedSettingsPath)
    .filter((alias) => alias !== path)));
  return {
    path,
    aliases,
    title: cleanSettingsText(route.title, route.label, fallbackLabel),
    label: cleanSettingsText(route.label, route.title, fallbackLabel),
    description: cleanSettingsText(route.description),
    icon: cleanSettingsText(route.icon, "settings"),
    section: settingsSection(route.section),
    render: route.render
  };
}

function normalizedSettingsPath(value: string): string {
  const path = value.trim().split(/[?#]/, 1)[0] || "/";
  if (path === "/") return path;
  return `/${path.replace(/^\/+|\/+$/g, "")}`;
}

function settingsLabelFromPath(path: string): string {
  const segment = path.split("/").filter(Boolean).at(-1) || "Plugin";
  return segment.split(/[-_]+/).filter(Boolean).map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function cleanSettingsText(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function settingsSection(value: unknown): SettingsSectionID {
  return value === "general" || value === "mail" || value === "preferences" || value === "plugins" ? value : "plugins";
}

function runtimePluginErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message.trim();
  return "Plugin module could not be imported.";
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
