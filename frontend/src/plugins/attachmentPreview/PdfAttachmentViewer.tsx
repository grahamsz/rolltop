export function PdfAttachmentViewer({ src }: { src: string }) {
  return <iframe className="pdf-preview-frame" src={src} title="PDF preview" />;
}
