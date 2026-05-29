// File overview: Attachment preview slot registry for optional preview plugins.

import { lazy, Suspense } from "react";
import type { Attachment } from "../../../frontend/src/types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../../../frontend/src/plugins/registry";
import { AttachmentPreviewAction } from "./AttachmentPreviewAction";
import { PdfAttachmentViewer } from "./PdfAttachmentViewer";

const LazyAttachmentPreviewAction = lazy(() =>
  import("./AttachmentPreviewAction").then((module) => ({ default: module.AttachmentPreviewAction }))
);

/** AttachmentPreviewSlot renders attachment preview UI only when the plugin and preview metadata are present. */
export function AttachmentPreviewSlot({ attachment, plugins }: { attachment: Attachment; plugins: PluginSet }) {
  if (!pluginEnabled(plugins, pluginIDs.attachmentPreview) || !attachment.preview?.available) return null;
  return (
    <Suspense fallback={null}>
      <LazyAttachmentPreviewAction attachment={attachment} />
    </Suspense>
  );
}

export { AttachmentPreviewAction, PdfAttachmentViewer };

export default {
  AttachmentPreviewSlot,
  AttachmentPreviewAction,
  PdfAttachmentViewer
};
