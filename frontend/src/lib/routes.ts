// File overview: Client-side route parser and URL builder. It owns the friendly mailbox/search
// slugs and preserves safe return URLs for message-detail back navigation.

import type { LocationState } from "../appTypes";

/** Read the browser URL into the tiny route state object used by App. */
export function currentLocation(): LocationState {
  return { path: window.location.pathname, search: window.location.search };
}

/** routeWithSearch preserves a path plus query string as a safe return URL candidate. */
export function routeWithSearch(path: string, search = ""): string {
  return `${path}${search}`;
}

function positiveInt(value: string | null | undefined, fallback: number): number {
  const raw = value || "";
  const number = raw.startsWith("p") ? raw.slice(1) : raw;
  const parsed = Number.parseInt(number, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function decodePathSegment(value = ""): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return "";
  }
}

/** Parse /mail, /mail/pN, /mailbox/:id, and /mailbox/:id/pN into list state. */
export function mailRoute(path: string): { mailboxID: string | null; page: number } {
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "mailbox") {
    const id = positiveInt(parts[1], 0);
    return { mailboxID: id > 0 ? String(id) : null, page: positiveInt(parts[2], 1) };
  }
  return { mailboxID: null, page: parts[0] === "mail" ? positiveInt(parts[1], 1) : 1 };
}

/** mailURL builds the friendly mailbox/all-mail list URL for a page. */
export function mailURL(mailboxID: string | number | null, page = 1): string {
  const suffix = page > 1 ? `/p${page}` : "";
  return mailboxID ? `/mailbox/${mailboxID}${suffix}` : `/mail${suffix}`;
}

/** Parse /search/q/:query/pN slugs into search state. */
export function searchRoute(path: string): { query: string; page: number } {
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "search" && parts[1]?.startsWith("p")) {
    return { query: "", page: positiveInt(parts[1], 1) };
  }
  if (parts[0] !== "search" || parts[1] !== "q") return { query: "", page: 1 };
  const query = decodePathSegment(parts[2]);
  let page = 1;
  for (const part of parts.slice(3)) {
    if (part.startsWith("p")) page = positiveInt(part, page);
  }
  return { query, page };
}

/** searchURL builds the friendly search URL for a query/page pair. */
export function searchURL(query: string, page = 1): string {
  const trimmed = query.trim();
  if (!trimmed) return page > 1 ? `/search/p${page}` : "/search";
  const pagePart = page > 1 ? `/p${page}` : "";
  return `/search/q/${encodeURIComponent(trimmed)}${pagePart}`;
}

/** Keep back links internal before they are reflected into message URLs. */
export function safeInternalURL(value: string | null | undefined, fallback = "/mail"): string {
  if (!value) return fallback;
  try {
    const url = new URL(value, window.location.origin);
    if (url.origin !== window.location.origin) return fallback;
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return fallback;
  }
}

/** messageBackURL extracts the safe return target from a message-detail URL. */
export function messageBackURL(location: LocationState): string {
  return safeInternalURL(new URLSearchParams(location.search).get("back"), "/mail");
}

/** messageURL builds a message-detail URL with search highlight terms and back target. */
export function messageURL(messageID: number, searchQuery = "", matchTerms: string[] = [], backURL = ""): string {
  const query = searchQuery.trim();
  if (!query && matchTerms.length === 0 && !backURL) return `/messages/${messageID}`;
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  matchTerms.slice(0, 10).forEach((term) => {
    if (term.trim()) params.append("term", term.trim());
  });
  if (backURL) params.set("back", safeInternalURL(backURL));
  return `/messages/${messageID}?${params}`;
}

/** messageHighlightQuery returns the raw query used to highlight message-detail text. */
export function messageHighlightQuery(location: LocationState): string {
  const params = new URLSearchParams(location.search);
  return params.get("q") || params.get("highlight") || "";
}

/** messageHighlightTerms returns explicit Bleve-reported terms carried by a message URL. */
export function messageHighlightTerms(location: LocationState): string[] {
  return new URLSearchParams(location.search).getAll("term");
}
