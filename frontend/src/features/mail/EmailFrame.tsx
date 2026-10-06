import { useEffect, useRef, useState } from "react";
import { highlightEmailDocument } from "../../lib/searchHighlight";
import { observeEmailFrameSize } from "../../lib/emailFrameSize";

function currentEmailDocumentTheme(): "classic" | "classic_dark" | "matrix" {
  const theme = document.documentElement.dataset.theme;
  return theme === "classic_dark" || theme === "matrix" ? theme : "classic";
}

function themedEmailSrcDoc(srcDoc: string): string {
  const theme = currentEmailDocumentTheme();
  if (theme === "classic") return srcDoc;
  return srcDoc.replace(/<html(\s|>)/i, `<html data-rolltop-theme="${theme}"$1`);
}

function applyEmailDocumentTheme(doc: Document | null | undefined) {
  if (!doc) return;
  const theme = currentEmailDocumentTheme();
  if (theme === "classic") {
    doc.documentElement.removeAttribute("data-rolltop-theme");
    return;
  }
  doc.documentElement.setAttribute("data-rolltop-theme", theme);
}

// EmailFrame isolates message HTML in a sandboxed iframe, applies the active
// Rolltop theme, highlights search terms inside the iframe document, and
// tracks content height as cards expand, screens resize, and images/fonts load.
export function EmailFrame({
  srcDoc,
  highlightQuery = "",
  highlightTerms = [],
  full = false
}: {
  srcDoc: string;
  highlightQuery?: string;
  highlightTerms?: string[];
  full?: boolean;
}) {
  const ref = useRef<HTMLIFrameElement | null>(null);
  const stopObserving = useRef<(() => void) | null>(null);
  const [height, setHeight] = useState(full ? 220 : 96);
  const highlightKey = `${highlightQuery}:${highlightTerms.join(",")}`;
  const themedSrcDoc = themedEmailSrcDoc(srcDoc);

  useEffect(() => {
    setHeight(full ? 220 : 96);
    return () => stopObserving.current?.();
  }, [srcDoc, full]);

  useEffect(() => {
    highlightEmailDocument(ref.current?.contentDocument, highlightQuery, highlightTerms);
  }, [highlightKey]);

  return (
    <iframe
      ref={ref}
      className={`email-frame ${full ? "full" : ""}`}
      srcDoc={themedSrcDoc}
      title="Email body"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      scrolling="no"
      style={{ height }}
      onLoad={() => {
        const doc = ref.current?.contentDocument;
        applyEmailDocumentTheme(doc);
        highlightEmailDocument(doc, highlightQuery, highlightTerms);
        stopObserving.current?.();
        if (ref.current) {
          stopObserving.current = observeEmailFrameSize(ref.current, full ? 180 : 84, setHeight);
        }
      }}
    />
  );
}
