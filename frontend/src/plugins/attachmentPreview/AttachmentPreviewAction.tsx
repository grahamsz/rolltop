import { lazy, Suspense, useEffect, useState } from "react";
import type { Attachment } from "../../types";
import { Icon } from "../../components/Icon";

const LazyPdfAttachmentViewer = lazy(() =>
  import("./PdfAttachmentViewer").then((module) => ({ default: module.PdfAttachmentViewer }))
);

export function AttachmentPreviewAction({ attachment }: { attachment: Attachment }) {
  const [open, setOpen] = useState(false);
  if (!attachment.preview?.available || !attachment.preview.url) return null;
  return (
    <>
      <button className="attachment-preview-link" type="button" onClick={() => setOpen(true)}>
        Preview
      </button>
      {open ? <AttachmentPreviewModal attachment={attachment} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function AttachmentPreviewModal({ attachment, onClose }: { attachment: Attachment; onClose: () => void }) {
  const preview = attachment.preview;
  const title = attachment.filename || "Attachment preview";

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
              <LazyPdfAttachmentViewer src={preview.url} />
            </Suspense>
          ) : (
            <img src={preview.url} alt={title} />
          )}
        </div>
      </section>
    </div>
  );
}
