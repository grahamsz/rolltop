// Measure content, not the iframe viewport: the document element's scrollHeight
// is at least the current frame height and cannot tell us when content shrinks.
export function observeEmailFrameSize(frame: HTMLIFrameElement, minimum: number, onHeight: (height: number) => void): () => void {
  const doc = frame.contentDocument;
  const body = doc?.body;
  if (!doc || !body) return () => {};

  let pending = 0;
  let disposed = false;
  const measure = () => {
    pending = 0;
    // Collapsed cards have no layout. ResizeObserver will retry on expansion.
    if (disposed || frame.clientWidth === 0) return;
    onHeight(Math.ceil(Math.max(minimum, body.scrollHeight, body.getBoundingClientRect().height)));
  };
  const schedule = () => {
    if (!disposed && !pending) pending = window.requestAnimationFrame(measure);
  };
  const observer = new ResizeObserver(schedule);
  observer.observe(body);
  observer.observe(frame);
  doc.addEventListener("load", schedule, true);
  doc.addEventListener("error", schedule, true);
  void doc.fonts?.ready.then(schedule);
  schedule();

  return () => {
    disposed = true;
    observer.disconnect();
    window.cancelAnimationFrame(pending);
    doc.removeEventListener("load", schedule, true);
    doc.removeEventListener("error", schedule, true);
  };
}
