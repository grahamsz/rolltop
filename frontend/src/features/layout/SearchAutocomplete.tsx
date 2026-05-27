// File overview: Search-box suggestion hook and popover. It combines route-aware search input state
// with plugin-provided language suggestions and folder shortcuts.

import { useEffect, useMemo, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, RefObject } from "react";
import { api } from "../../api";
import type { ContactAutocomplete, Mailbox } from "../../types";
import { languageSearchSuggestions } from "../../plugins/languageSearch/suggestions";
import type { PluginSet } from "../../plugins/registry";

type SearchAutocompleteItem = {
  value: string;
  token: string;
  label: string;
  detail: string;
  kind: "operator" | "contact" | "folder" | "date" | "state";
};

type ActiveToken = {
  token: string;
  start: number;
  end: number;
  field: string;
  value: string;
};

const operatorSuggestions = [
  { value: "from:", label: "from:", detail: "sender contact or domain" },
  { value: "to:", label: "to:", detail: "recipient contact" },
  { value: "cc:", label: "cc:", detail: "copied contact" },
  { value: "in:", label: "in:", detail: "folder" },
  { value: "before:", label: "before:", detail: "date" },
  { value: "after:", label: "after:", detail: "date" },
  { value: "year:", label: "year:", detail: "calendar year" },
  { value: "is:", label: "is:", detail: "read, unread, starred" },
  { value: "has:", label: "has:", detail: "attachment" },
  { value: "subject:", label: "subject:", detail: "subject text" },
  { value: "filename:", label: "filename:", detail: "attachment name" }
];

const stateValues = ["read", "unread", "starred", "notstarred"];
const currentYear = new Date().getFullYear();

/** useSearchAutocomplete builds keyboard-navigable suggestions for the global search input. */
export function useSearchAutocomplete({
  query,
  focused,
  inputRef,
  mailboxes,
  pluginSet,
  setQuery
}: {
  query: string;
  focused: boolean;
  inputRef: RefObject<HTMLInputElement | null>;
  mailboxes: Mailbox[];
  pluginSet: PluginSet;
  setQuery: (query: string) => void;
}) {
  const [activeIndex, setActiveIndex] = useState(0);
  const activeToken = useMemo(() => getActiveToken(query, inputRef.current?.selectionStart ?? query.length), [query, inputRef]);
  const contactSearch = isContactField(activeToken.field) ? unquoteSearchValue(activeToken.value) : "";
  const [contacts, setContacts] = useState<ContactAutocomplete[]>([]);

  useEffect(() => {
    if (!focused || !isContactField(activeToken.field)) {
      setContacts([]);
      return;
    }
    let cancelled = false;
    const handle = window.setTimeout(() => {
      api.contactAutocomplete(contactSearch)
        .then((res) => {
          if (!cancelled) setContacts(res.contacts);
        })
        .catch(() => {
          if (!cancelled) setContacts([]);
        });
    }, contactSearch ? 120 : 0);
    return () => {
      cancelled = true;
      window.clearTimeout(handle);
    };
  }, [activeToken.field, contactSearch, focused]);

  const items = useMemo(
    () => buildSearchAutocompleteItems(activeToken, contacts, mailboxes, pluginSet),
    [activeToken, contacts, mailboxes, pluginSet]
  );

  useEffect(() => {
    setActiveIndex(0);
  }, [activeToken.token, items.length]);

  function choose(item: SearchAutocompleteItem) {
    const next = `${query.slice(0, activeToken.start)}${item.value}${query.slice(activeToken.end)}`;
    const cursor = activeToken.start + item.value.length;
    setQuery(next);
    window.requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.setSelectionRange(cursor, cursor);
    });
  }

  function onKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (items.length === 0) return;
    if (event.key === "Tab") {
      event.preventDefault();
      choose(items[activeIndex] ?? items[0]);
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActiveIndex((index) => (index + 1) % items.length);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setActiveIndex((index) => (index - 1 + items.length) % items.length);
    }
  }

  return { activeIndex, items, choose, onKeyDown };
}

/** SearchAutocomplete renders the suggestion popover under the topbar search field. */
export function SearchAutocomplete({
  items,
  activeIndex,
  onChoose
}: {
  items: SearchAutocompleteItem[];
  activeIndex: number;
  onChoose: (item: SearchAutocompleteItem) => void;
}) {
  if (items.length === 0) return null;
  return (
    <div className="search-autocomplete" role="listbox" aria-label="Search completions">
      <div className="search-autocomplete-tabs">
        {items.map((item, index) => (
          <button
            type="button"
            className={`search-autocomplete-tab ${index === activeIndex ? "active" : ""}`}
            key={`${item.kind}:${item.value}`}
            role="option"
            aria-selected={index === activeIndex}
            onMouseDown={(event) => {
              event.preventDefault();
              onChoose(item);
            }}
          >
            <code>{item.label}</code>
            <span>{item.detail}</span>
          </button>
        ))}
      </div>
      <span className="search-autocomplete-hint">Tab completes</span>
    </div>
  );
}

function buildSearchAutocompleteItems(
  active: ActiveToken,
  contacts: ContactAutocomplete[],
  mailboxes: Mailbox[],
  pluginSet: PluginSet
): SearchAutocompleteItem[] {
  if (!active.token.trim()) return [];
  if (!active.field) {
    const lower = active.token.toLocaleLowerCase();
    const languageEnabled = languageSearchSuggestions(pluginSet).length > 0;
    const operators = languageEnabled
      ? [...operatorSuggestions, { value: "lang:", label: "lang:", detail: "message language" }]
      : operatorSuggestions;
    return operators
      .filter((item) => item.value.startsWith(lower))
      .map((item) => ({ ...item, token: active.token, kind: "operator" as const }));
  }

  const value = unquoteSearchValue(active.value);
  const valueLower = value.toLocaleLowerCase();
  switch (active.field) {
    case "from":
    case "to":
    case "cc":
      return contacts.slice(0, 8).map((contact) => ({
        value: `${active.field}:${quoteSearchValue(contact.email)} `,
        token: active.token,
        label: contact.email,
        detail: contact.name || contact.label || "contact",
        kind: "contact" as const
      }));
    case "in":
      return mailboxes
        .filter((mailbox) => mailboxSearchText(mailbox).includes(valueLower))
        .slice(0, 10)
        .map((mailbox) => ({
          value: `in:${quoteSearchValue(mailbox.name)} `,
          token: active.token,
          label: mailbox.name,
          detail: mailbox.account_label || mailbox.account_email || "folder",
          kind: "folder" as const
        }));
    case "before":
    case "after":
      return dateSamples(value).map((sample) => ({
        value: `${active.field}:${sample} `,
        token: active.token,
        label: `${active.field}:${sample}`,
        detail: "sample date",
        kind: "date" as const
      }));
    case "year":
      return [currentYear, currentYear - 1, 2024]
        .filter((year, index, years) => years.indexOf(year) === index)
        .map(String)
        .filter((year) => year.startsWith(value))
        .map((year) => ({
          value: `year:${year} `,
          token: active.token,
          label: `year:${year}`,
          detail: "calendar year",
          kind: "date" as const
        }));
    case "is":
      return stateValues
        .filter((state) => state.startsWith(valueLower))
        .map((state) => ({
          value: `is:${state} `,
          token: active.token,
          label: `is:${state}`,
          detail: "message state",
          kind: "state" as const
        }));
    case "has":
      return "attachment".startsWith(valueLower)
        ? [{ value: "has:attachment ", token: active.token, label: "has:attachment", detail: "attachments", kind: "state" as const }]
        : [];
    case "lang":
      return languageSearchSuggestions(pluginSet)
        .filter(([completion]) => completion.slice("lang:".length).startsWith(valueLower))
        .map(([completion, detail]) => ({
          value: completion,
          token: active.token,
          label: completion.trim(),
          detail,
          kind: "state" as const
        }));
    default:
      return [];
  }
}

function getActiveToken(query: string, cursor: number): ActiveToken {
  const safeCursor = Math.max(0, Math.min(cursor, query.length));
  let start = safeCursor;
  while (start > 0 && !/\s/u.test(query[start - 1])) start--;
  let end = safeCursor;
  while (end < query.length && !/\s/u.test(query[end])) end++;
  const token = query.slice(start, end);
  const colon = token.indexOf(":");
  if (colon <= 0) return { token, start, end, field: "", value: "" };
  return {
    token,
    start,
    end,
    field: token.slice(0, colon).toLocaleLowerCase(),
    value: token.slice(colon + 1)
  };
}

function isContactField(field: string): boolean {
  return field === "from" || field === "to" || field === "cc";
}

function quoteSearchValue(value: string): string {
  const clean = value.trim().replace(/"/g, "");
  return /\s/u.test(clean) ? `"${clean}"` : clean;
}

function unquoteSearchValue(value: string): string {
  return value.trim().replace(/^"|"$/g, "");
}

function mailboxSearchText(mailbox: Mailbox): string {
  return [mailbox.name, mailbox.role, mailbox.account_label, mailbox.account_email].join(" ").toLocaleLowerCase();
}

function dateSamples(value: string): string[] {
  return [`${currentYear}/01/01`, "today", "yesterday"].filter((sample) => sample.startsWith(value.toLocaleLowerCase()));
}
