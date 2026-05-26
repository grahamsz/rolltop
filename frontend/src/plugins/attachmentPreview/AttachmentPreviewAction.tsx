// File overview: Attachment preview launcher. It renders preview actions for files that have a
// supported frontend preview kind.

import { lazy, Suspense, useEffect, useState } from "react";
import type { Attachment } from "../../types";
import { Icon } from "../../components/Icon";
import pdfiumWasmUrl from "@embedpdf/snippet/dist/pdfium.wasm?url";

const loadPdfAttachmentViewer = () =>
  import("./PdfAttachmentViewer").then((module) => ({ default: module.PdfAttachmentViewer }));

const LazyPdfAttachmentViewer = lazy(loadPdfAttachmentViewer);
const PDFIUM_WASM_PRELOAD_ID = "mailmirror-pdfium-wasm-preload";

function preloadPdfAttachmentViewer() {
  void loadPdfAttachmentViewer();
  preloadPdfiumWasm();
}

function preloadPdfiumWasm() {
  if (typeof document === "undefined" || document.getElementById(PDFIUM_WASM_PRELOAD_ID)) return;
  const link = document.createElement("link");
  link.id = PDFIUM_WASM_PRELOAD_ID;
  link.rel = "preload";
  link.as = "fetch";
  link.href = pdfiumWasmUrl;
  link.type = "application/wasm";
  link.crossOrigin = "anonymous";
  document.head.appendChild(link);
}

/** AttachmentPreviewAction opens the available preview for a supported attachment. */
export function AttachmentPreviewAction({ attachment }: { attachment: Attachment }) {
  const [open, setOpen] = useState(false);
  const preview = attachment.preview;
  const isPdfPreview = preview?.available === true && Boolean(preview.url) && preview.kind === "pdf";

  useEffect(() => {
    if (isPdfPreview) preloadPdfAttachmentViewer();
  }, [isPdfPreview]);

  if (!preview?.available || !preview.url) return null;

  const openPreview = () => {
    if (isPdfPreview) preloadPdfAttachmentViewer();
    setOpen(true);
  };

  return (
    <>
      <button
        className="attachment-preview-link"
        type="button"
        onClick={openPreview}
        onFocus={isPdfPreview ? preloadPdfAttachmentViewer : undefined}
        onPointerEnter={isPdfPreview ? preloadPdfAttachmentViewer : undefined}
      >
        Preview
      </button>
      {open ? <AttachmentPreviewModal attachment={attachment} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function AttachmentPreviewModal({ attachment, onClose }: { attachment: Attachment; onClose: () => void }) {
  const preview = attachment.preview;
  const title = attachment.filename || "Attachment preview";
  const pdfSearchTerms = attachment.match_terms ?? [];

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  if (!preview?.url) return null;
  return (
    <div className="attachment-preview-backdrop" role="presentation" onClick={onClose}>
      <section
        className={`attachment-preview-modal ${preview.kind === "pdf" ? "pdf" : "image"}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(event) => event.stopPropagation()}
      >
        <header className="attachment-preview-head">
          <div>
            <h2>{title}</h2>
            <span>{preview.kind.toUpperCase()}</span>
          </div>
          <div className="attachment-preview-actions">
            <a className="button secondary" href={preview.url} target="_blank" rel="noreferrer">
              Open
            </a>
            <button className="ghost" type="button" onClick={onClose} title="Close preview">
              <Icon name="close" />
            </button>
          </div>
        </header>
        <div className="attachment-preview-body">
          {preview.kind === "pdf" ? (
            <Suspense fallback={<div className="attachment-preview-loading">Loading PDF viewer...</div>}>
              <LazyPdfAttachmentViewer src={preview.url} terms={pdfSearchTerms} />
            </Suspense>
          ) : (
            <img src={preview.url} alt={title} />
          )}
        </div>
      </section>
    </div>
  );
}
