// File overview: EmbedPDF-based PDF preview. The component fetches the PDF bytes itself so
// failures surface in our UI before handing the document to the embedded viewer.

import { useEffect, useMemo, useState } from "react";
import { PDFViewer, type PDFViewerConfig } from "@embedpdf/react-pdf-viewer";
import pdfiumWasmUrl from "@embedpdf/snippet/dist/pdfium.wasm?url";

type PdfFetchState =
  | { status: "loading" }
  | { status: "ready"; buffer: ArrayBuffer; name: string }
  | { status: "error"; message: string };

const EMPTY_PDF_FONT_FALLBACK = { fonts: {} } satisfies NonNullable<PDFViewerConfig["fontFallback"]>;

type PdfAttachmentViewerProps = {
  src: string;
};

/** PdfAttachmentViewer renders an attachment PDF inside the EmbedPDF viewer. */
export function PdfAttachmentViewer({ src }: PdfAttachmentViewerProps) {
  const [pdfFetch, setPdfFetch] = useState<PdfFetchState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    setPdfFetch({ status: "loading" });

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

  if (pdfFetch.status === "loading") {
    return <div className="attachment-preview-loading">Loading PDF...</div>;
  }

  if (pdfFetch.status === "error") {
    return <div className="attachment-preview-error">{pdfFetch.message}</div>;
  }

  return (
    <div className="pdf-preview-shell">
      {config ? <PDFViewer key={src} className="pdf-preview-frame" config={config} /> : null}
    </div>
  );
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
