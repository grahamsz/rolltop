import type { Bootstrap, ChromeEvent } from "../types";

type BuildIdentitySource = Pick<Bootstrap | ChromeEvent, "build_version" | "build_date" | "build_label" | "build_commit">;

export function serverBuildIdentity(source: BuildIdentitySource | null | undefined): string {
  if (!source) return "";
  const version = source.build_version?.trim() || "";
  const release = source.build_date?.trim() || source.build_label?.trim() || "";
  const commit = source.build_commit?.trim() || "";
  if (commit) return `${version}:${release}:${commit}`;
  return release ? `${version}:${release}` : "";
}

export function shellAssetSignature(root: ParentNode, baseURL = window.location.href): string {
  const base = new URL(baseURL);
  const assets = Array.from(root.querySelectorAll<HTMLScriptElement | HTMLLinkElement>(
    'script[type="module"][src], link[rel="stylesheet"][href]'
  )).flatMap((element) => {
    const value = element instanceof HTMLScriptElement ? element.getAttribute("src") : element.getAttribute("href");
    if (!value) return [];
    try {
      const url = new URL(value, base);
      return url.origin === base.origin && url.pathname.startsWith("/assets/")
        ? [`${url.pathname}${url.search}`]
        : [];
    } catch {
      return [];
    }
  });
  return Array.from(new Set(assets)).sort().join("|");
}

export async function serverShellDiffers(): Promise<boolean> {
  const loaded = shellAssetSignature(document);
  if (!loaded) return false;
  try {
    const response = await fetch(`${window.location.pathname}${window.location.search}`, {
      cache: "no-store",
      credentials: "same-origin",
      headers: { Accept: "text/html" }
    });
    if (!response.ok || !response.headers.get("Content-Type")?.toLowerCase().includes("text/html")) return false;
    const candidate = new DOMParser().parseFromString(await response.text(), "text/html");
    const current = shellAssetSignature(candidate);
    return Boolean(current && current !== loaded);
  } catch {
    return false;
  }
}
