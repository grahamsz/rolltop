import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

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
