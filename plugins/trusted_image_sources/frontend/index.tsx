// File overview: Per-sender image trust action displayed inside the remote-image notice.

import type { ThreadMessage } from "../../../frontend/src/types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../../../frontend/src/plugins/registry";

/** TrustImageSourceAction renders the per-sender remote-image trust button when enabled. */
export function TrustImageSourceAction({
  item,
  plugins,
  onTrustImages
}: {
  item: ThreadMessage;
  plugins: PluginSet;
  onTrustImages: () => void | Promise<void>;
}) {
  if (!pluginEnabled(plugins, pluginIDs.trustedImageSources) || !item.has_remote_images || item.images_allowed) {
    return null;
  }
  return (
    <button className="secondary" type="button" onClick={() => void onTrustImages()}>
      Always show from this sender
    </button>
  );
}

export default {
  TrustImageSourceAction
};
