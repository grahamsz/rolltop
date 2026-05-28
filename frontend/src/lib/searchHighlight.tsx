// File overview: Search highlighting helpers for both React text nodes and sandboxed email iframes.
// They combine the original query with Bleve-reported match terms from the API.

/**
 * Turn a user query plus Bleve-reported terms into safe highlight needles.
 * Operators that filter rather than match text are skipped, and longer terms are
 * sorted first to keep compound matches from being split by shorter words.
 */
export function searchHighlightTerms(query: string, extraTerms: string[] = []): string[] {
  const seen = new Set<string>();
  const terms: string[] = [];
  const add = (raw: string) => {
    const value = raw.trim().replace(/^[`"'~*()[\]{}<>.,;:]+|[`"'~*()[\]{}<>.,;:]+$/g, "");
    if (!value) return;
    const lower = value.toLocaleLowerCase();
    if (["and", "or", "not"].includes(lower) || seen.has(lower)) return;
    seen.add(lower);
    terms.push(value);
  };

  for (const token of searchQueryTokens(query)) {
    const fieldIndex = token.indexOf(":");
    let value = token;
    if (fieldIndex > 0) {
      const field = token.slice(0, fieldIndex).trim().toLocaleLowerCase();
      if (["after", "before", "newer", "older", "year", "in", "has", "is", "lang"].includes(field)) continue;
      value = token.slice(fieldIndex + 1);
    }
    add(value);
    value.split(/[^\p{L}\p{N}]+/u).forEach((word) => {
      if ([...word].length >= 3) add(word);
    });
  }
  extraTerms.forEach(add);
  return terms.sort((a, b) => [...b].length - [...a].length).slice(0, 16);
}

function searchQueryTokens(query: string): string[] {
  const tokens: string[] = [];
  let index = 0;
  while (index < query.length) {
    while (index < query.length && /\s/u.test(query[index])) index++;
    if (index >= query.length) break;
    let token = "";
    let quote = "";
    while (index < query.length) {
      const char = query[index];
      if (quote) {
        if (char === quote) {
          quote = "";
        } else {
          token += char;
        }
        index++;
        continue;
      }
      if (char === "\"" || char === "'") {
        quote = char;
        index++;
        continue;
      }
      if (/\s/u.test(char)) break;
      token += char;
      index++;
    }
    if (token.trim()) tokens.push(token.trim());
  }
  return tokens;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** highlightRegExp compiles query and match terms into one safe global regex. */
export function highlightRegExp(query: string, extraTerms: string[] = []): RegExp | null {
  const terms = searchHighlightTerms(query, extraTerms);
  if (terms.length === 0) return null;
  try {
    return new RegExp(`(${terms.map(escapeRegExp).join("|")})`, "giu");
  } catch {
    return null;
  }
}

/** HighlightedText wraps matching text fragments in themed mark elements for React-rendered strings. */
export function HighlightedText({ text, query, terms = [] }: { text: string; query: string; terms?: string[] }) {
  const pattern = highlightRegExp(query, terms);
  if (!pattern || !text) return <>{text}</>;
  const parts = text.split(pattern);
  return (
    <>
      {parts.map((part, index) =>
        part === "" ? null : index % 2 === 1 ? (
          <mark className="search-hit" key={`${part}-${index}`}>
            {part}
          </mark>
        ) : (
          <span key={`${part}-${index}`}>{part}</span>
        )
      )}
    </>
  );
}

function highlightImageAltMatches(doc: Document, pattern: RegExp) {
  for (const image of Array.from(doc.images)) {
    const alt = image.getAttribute("alt") || "";
    pattern.lastIndex = 0;
    if (alt && pattern.test(alt)) {
      image.classList.add("rolltop-search-image-hit");
      image.setAttribute("data-rolltop-alt-hit", "true");
    } else {
      image.classList.remove("rolltop-search-image-hit");
      image.removeAttribute("data-rolltop-alt-hit");
    }
  }
  pattern.lastIndex = 0;
}

/** Highlight search terms inside a sandboxed email iframe after it has loaded. */
export function highlightEmailDocument(doc: Document | null | undefined, query: string, terms: string[] = []) {
  if (!doc || (!query.trim() && terms.length === 0)) return;
  const body = doc.body;
  if (!body) return;
  const pattern = highlightRegExp(query, terms);
  if (!pattern) return;
  if (!doc.head.querySelector("[data-rolltop-highlight-style]")) {
    const style = doc.createElement("style");
    style.setAttribute("data-rolltop-highlight-style", "true");
    style.textContent = "mark.rolltop-search-hit{background:rgba(229,169,40,.26);color:#202426;border-radius:3px;padding:0 1px;box-shadow:none}img.rolltop-search-image-hit{outline:2px solid rgba(229,169,40,.92)!important;outline-offset:2px!important;box-shadow:0 0 0 4px rgba(229,169,40,.20)!important;border-radius:4px}html[data-rolltop-theme=\"classic_dark\"] mark.rolltop-search-hit,html[data-rolltop-theme=\"matrix\"] mark.rolltop-search-hit{background:rgba(224,182,77,.28);color:#f5fff8}html[data-rolltop-theme=\"classic_dark\"] img.rolltop-search-image-hit,html[data-rolltop-theme=\"matrix\"] img.rolltop-search-image-hit{outline-color:rgba(224,182,77,.95)!important;box-shadow:0 0 0 4px rgba(224,182,77,.22)!important}";
    doc.head.appendChild(style);
  }
  highlightImageAltMatches(doc, pattern);
  const blocked = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "TEXTAREA", "MARK"]);
  const walker = doc.createTreeWalker(body, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = node.parentElement;
      if (!parent || blocked.has(parent.tagName)) return NodeFilter.FILTER_REJECT;
      if (!node.nodeValue || !pattern.test(node.nodeValue)) return NodeFilter.FILTER_REJECT;
      pattern.lastIndex = 0;
      return NodeFilter.FILTER_ACCEPT;
    }
  });
  const nodes: Text[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    nodes.push(node as Text);
  }
  for (const node of nodes) {
    const value = node.nodeValue || "";
    pattern.lastIndex = 0;
    const fragment = doc.createDocumentFragment();
    let lastIndex = 0;
    for (const match of value.matchAll(pattern)) {
      const index = match.index ?? 0;
      if (index > lastIndex) fragment.appendChild(doc.createTextNode(value.slice(lastIndex, index)));
      const mark = doc.createElement("mark");
      mark.className = "rolltop-search-hit";
      mark.textContent = match[0];
      fragment.appendChild(mark);
      lastIndex = index + match[0].length;
    }
    if (lastIndex < value.length) fragment.appendChild(doc.createTextNode(value.slice(lastIndex)));
    node.parentNode?.replaceChild(fragment, node);
  }
}
