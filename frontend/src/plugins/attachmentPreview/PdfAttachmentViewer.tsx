// File overview: EmbedPDF-based PDF preview. The component fetches the PDF bytes itself so
// failures surface in our UI, then asks EmbedPDF to highlight search hits in-place.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  PDFViewer,
  type PDFViewerConfig,
  type PluginRegistry,
  type SearchCapability,
} from "@embedpdf/react-pdf-viewer";
import pdfiumWasmUrl from "@embedpdf/snippet/dist/pdfium.wasm?url";

type PdfFetchState =
  | { status: "loading" }
  | { status: "ready"; buffer: ArrayBuffer; name: string }
  | { status: "error"; message: string };
type PdfSearchStatus = "idle" | "searching" | "found" | "missing" | "error";

const PDF_SEARCH_RETRIES = 6;
const PDF_SEARCH_RETRY_DELAY_MS = 250;
const PDF_SEARCH_READY_TIMEOUT_MS = 4000;
const PDF_SEARCH_READY_POLL_MS = 100;
const MAX_PDF_SEARCH_TERMS = 8;
const EMPTY_PDF_SEARCH_TERMS: string[] = [];
const EMPTY_PDF_FONT_FALLBACK = { fonts: {} } satisfies NonNullable<PDFViewerConfig["fontFallback"]>;

type PdfAttachmentViewerProps = {
  src: string;
  terms?: string[];
};

type DocumentManagerCapabilitySubset = {
  getOpenDocuments: () => Array<{ status?: string }>;
  onDocumentOpened: (listener: () => void) => () => void;
  onDocumentError: (listener: (event: { message?: string }) => void) => () => void;
};

type DocumentManagerProvider = {
  provides: () => DocumentManagerCapabilitySubset;
};

/** PdfAttachmentViewer renders an attachment PDF and asks EmbedPDF to highlight search hits. */
export function PdfAttachmentViewer({ src, terms }: PdfAttachmentViewerProps) {
  const searchTerms = useMemo(() => normalizePdfSearchTerms(terms ?? EMPTY_PDF_SEARCH_TERMS), [terms]);
  const shellRef = useRef<HTMLDivElement | null>(null);
  const [pdfFetch, setPdfFetch] = useState<PdfFetchState>({ status: "loading" });
  const [registry, setRegistry] = useState<PluginRegistry | null>(null);
  const [documentLoaded, setDocumentLoaded] = useState(false);
  const [viewerSearchReady, setViewerSearchReady] = useState(false);
  const [status, setStatus] = useState<PdfSearchStatus>("idle");
  const [activeTerm, setActiveTerm] = useState(searchTerms[0] ?? "");
  const [matchCount, setMatchCount] = useState<number | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setPdfFetch({ status: "loading" });
    setRegistry(null);
    setDocumentLoaded(false);
    setViewerSearchReady(false);
    setStatus("idle");
    setMatchCount(null);

    fetch(src, { credentials: "same-origin", signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`PDF request failed with ${response.status}`);
        const contentType = response.headers.get("Content-Type") ?? "";
        if (contentType && !contentType.toLowerCase().includes("application/pdf")) {
          throw new Error(`PDF request returned ${contentType}`);
        }
        const buffer = await response.arrayBuffer();
        if (buffer.byteLength === 0) throw new Error("PDF response was empty");
        if (!controller.signal.aborted) setPdfFetch({ status: "ready", buffer, name: pdfNameFromURL(src) });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setPdfFetch({ status: "error", message: error instanceof Error ? error.message : "PDF request failed" });
      });

    return () => controller.abort();
  }, [src]);

  const config = useMemo<PDFViewerConfig | null>(() => {
    if (pdfFetch.status !== "ready") return null;
    return {
      wasmUrl: pdfiumWasmUrl,
      fontFallback: EMPTY_PDF_FONT_FALLBACK,
      tabBar: "never",
      disabledCategories: ["annotation", "redaction", "insert", "stamp", "signature", "print", "export"],
      documentManager: {
        initialDocuments: [{ buffer: pdfFetch.buffer, name: pdfFetch.name, autoActivate: true }],
      },
      search: { showAllResults: true },
      stamp: { manifests: [], defaultLibrary: false },
      theme: { preference: "system" },
      fonts: {
        ui: {
          family: "system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif",
          stylesheetUrl: null,
        },
        signature: null,
      },
    };
  }, [pdfFetch]);

  const onReady = useCallback((readyRegistry: PluginRegistry) => {
    setRegistry(readyRegistry);
  }, []);

  useEffect(() => {
    setStatus("idle");
    setActiveTerm(searchTerms[0] ?? "");
    setMatchCount(null);
  }, [searchTerms, src]);

  useEffect(() => {
    if (!registry || pdfFetch.status !== "ready") return;
    const manager = getDocumentManagerCapability(registry);
    if (!manager) return;

    if (manager.getOpenDocuments().some((documentState) => documentState.status === "loaded")) {
      setDocumentLoaded(true);
    }
    const unsubscribeOpened = manager.onDocumentOpened(() => {
      setDocumentLoaded(true);
    });
    const unsubscribeError = manager.onDocumentError(() => {
      setStatus("error");
    });
    return () => {
      unsubscribeOpened();
      unsubscribeError();
    };
  }, [registry, pdfFetch]);

  useEffect(() => {
    setViewerSearchReady(false);
    if (!documentLoaded) return;

    let cancelled = false;
    let retryTimer: number | undefined;
    let timeoutTimer: number | undefined;
    let frameTimer: number | undefined;
    let nestedFrameTimer: number | undefined;

    const markReady = () => {
      if (cancelled) return;
      if (timeoutTimer !== undefined) window.clearTimeout(timeoutTimer);
      setViewerSearchReady(true);
    };

    const pollForRenderedPage = () => {
      if (cancelled) return;
      if (pdfViewerHasRenderedPage(shellRef.current)) {
        markReady();
        return;
      }
      retryTimer = window.setTimeout(pollForRenderedPage, PDF_SEARCH_READY_POLL_MS);
    };

    timeoutTimer = window.setTimeout(markReady, PDF_SEARCH_READY_TIMEOUT_MS);
    frameTimer = window.requestAnimationFrame(() => {
      nestedFrameTimer = window.requestAnimationFrame(pollForRenderedPage);
    });

    return () => {
      cancelled = true;
      if (retryTimer !== undefined) window.clearTimeout(retryTimer);
      if (timeoutTimer !== undefined) window.clearTimeout(timeoutTimer);
      if (frameTimer !== undefined) window.cancelAnimationFrame(frameTimer);
      if (nestedFrameTimer !== undefined) window.cancelAnimationFrame(nestedFrameTimer);
    };
  }, [documentLoaded, src]);

  useEffect(() => {
    if (!registry || !viewerSearchReady || searchTerms.length === 0) return;

    let cancelled = false;
    let retryTimer: number | undefined;

    const scheduleRetry = (termIndex: number, attempt: number) => {
      if (attempt >= PDF_SEARCH_RETRIES) {
        runSearchTerm(termIndex + 1, 0);
        return;
      }
      retryTimer = window.setTimeout(() => runSearchTerm(termIndex, attempt + 1), PDF_SEARCH_RETRY_DELAY_MS);
    };

    const runSearchTerm = (termIndex: number, attempt: number) => {
      if (cancelled) return;
      if (termIndex >= searchTerms.length) {
        setStatus("missing");
        setMatchCount(0);
        return;
      }

      const term = searchTerms[termIndex];
      const search = getSearchCapability(registry);
      if (!search) {
        scheduleRetry(termIndex, attempt);
        return;
      }

      setActiveTerm(term);
      setStatus("searching");
      try {
        search.startSearch();
        search.setShowAllResults(true);
        search
          .searchAllPages(term)
          .toPromise()
          .then((result) => {
            if (cancelled) return;
            if (result.total > 0) {
              search.goToResult(0);
              setMatchCount(result.total);
              setStatus("found");
              return;
            }
            runSearchTerm(termIndex + 1, 0);
          })
          .catch(() => {
            if (cancelled) return;
            scheduleRetry(termIndex, attempt);
          });
      } catch {
        scheduleRetry(termIndex, attempt);
      }
    };

    runSearchTerm(0, 0);

    return () => {
      cancelled = true;
      if (retryTimer !== undefined) window.clearTimeout(retryTimer);
      const search = getSearchCapability(registry);
      try {
        search?.stopSearch();
      } catch {
        // The EmbedPDF registry may already be tearing down when the modal closes.
      }
    };
  }, [registry, viewerSearchReady, searchTerms]);

  if (pdfFetch.status === "loading") {
    return <div className="attachment-preview-loading">Loading PDF...</div>;
  }

  if (pdfFetch.status === "error") {
    return <div className="attachment-preview-error">{pdfFetch.message}</div>;
  }

  return (
    <div className="pdf-preview-shell" ref={shellRef}>
      {config ? <PDFViewer key={src} className="pdf-preview-frame" config={config} onReady={onReady} /> : null}
      {status !== "idle" ? <PdfSearchStatusBadge status={status} term={activeTerm} matches={matchCount} /> : null}
    </div>
  );
}

function pdfViewerHasRenderedPage(shell: HTMLDivElement | null): boolean {
  const container = shell?.querySelector("embedpdf-container") as (HTMLElement & { shadowRoot?: ShadowRoot | null }) | null;
  const root = container?.shadowRoot;
  if (!root) return false;

  const documentContent = root.querySelector("#document-content") as HTMLElement | null;
  if (!documentContent) return false;

  const bounds = documentContent.getBoundingClientRect();
  if (bounds.width <= 0 || bounds.height <= 0) return false;

  const visibleText = (documentContent.textContent ?? "").toLocaleLowerCase();
  if (visibleText.includes("loading document") || visibleText.includes("initializing")) return false;

  const image = root.querySelector("#document-content img") as HTMLImageElement | null;
  if (image) {
    const imageBounds = image.getBoundingClientRect();
    return image.complete && image.naturalWidth > 0 && imageBounds.width > 0 && imageBounds.height > 0;
  }

  const canvas = root.querySelector("#document-content canvas") as HTMLCanvasElement | null;
  if (canvas) {
    const canvasBounds = canvas.getBoundingClientRect();
    return canvas.width > 0 && canvas.height > 0 && canvasBounds.width > 0 && canvasBounds.height > 0;
  }

  const page = root.querySelector("#document-content [data-page-index]") as HTMLElement | null;
  if (!page) return false;
  const pageBounds = page.getBoundingClientRect();
  return pageBounds.width > 0 && pageBounds.height > 0;
}

function PdfSearchStatusBadge({ status, term, matches }: { status: PdfSearchStatus; term: string; matches: number | null }) {
  if (!term) return null;
  let label = `Finding ${term}`;
  if (status === "found") label = `${matches ?? 0} PDF match${matches === 1 ? "" : "es"}: ${term}`;
  if (status === "missing") label = `No PDF text match: ${term}`;
  if (status === "error") label = `Could not search PDF: ${term}`;
  return <div className={`pdf-preview-status ${status}`}>{label}</div>;
}

type SearchProvider = {
  provides: () => SearchCapability;
};

function getSearchCapability(registry: PluginRegistry): SearchCapability | null {
  const provider = registry.getCapabilityProvider("search") as SearchProvider | null;
  if (!provider || typeof provider.provides !== "function") return null;
  return provider.provides();
}

function getDocumentManagerCapability(registry: PluginRegistry): DocumentManagerCapabilitySubset | null {
  const provider = registry.getCapabilityProvider("document-manager") as DocumentManagerProvider | null;
  if (!provider || typeof provider.provides !== "function") return null;
  return provider.provides();
}

function normalizePdfSearchTerms(values: string[]): string[] {
  const seen = new Set<string>();
  const terms: string[] = [];
  for (const rawValue of values) {
    const raw = rawValue.trim();
    if (!raw || raw.startsWith("-")) continue;
    const term = raw.replace(/^["'`]+|["'`]+$/g, "").replace(/\s+/g, " ").trim();
    if (term.length < 2) continue;
    const key = term.toLocaleLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    terms.push(term);
  }
  terms.sort((a, b) => b.length - a.length);
  return terms.slice(0, MAX_PDF_SEARCH_TERMS);
}

function pdfNameFromURL(value: string): string {
  try {
    const url = new URL(value, window.location.href);
    const last = url.pathname.split("/").filter(Boolean).pop();
    if (last) return decodeURIComponent(last);
  } catch {
    // Fall through to a stable display name.
  }
  return "attachment.pdf";
}
