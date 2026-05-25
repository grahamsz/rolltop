// File overview: Remote-image warning wrapper shown when a message has blocked external images.

import type { ReactNode } from "react";
import { Icon } from "../../components/Icon";
import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

/** RemoteImageNotice wraps message content with a prompt when remote images are blocked. */
export function RemoteImageNotice({
  item,
  plugins,
  onShowImages,
  children
}: {
  item: ThreadMessage;
  plugins: PluginSet;
  onShowImages: () => void;
  children?: ReactNode;
}) {
  if (!pluginEnabled(plugins, pluginIDs.remoteImageBlocklist) || !item.has_remote_images || item.images_allowed) {
    return null;
  }
  return (
    <div className="image-notice">
      <Icon name="image" />
      <span>Remote images are blocked for this sender.</span>
      <button className="secondary" type="button" onClick={onShowImages}>Show images</button>
      {children}
    </div>
  );
}
