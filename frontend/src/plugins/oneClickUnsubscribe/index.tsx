// File overview: One-click unsubscribe UI hooks. They render inline/menu actions only when both the
// plugin and the current message expose RFC8058 metadata.

import { Icon } from "../../components/Icon";
import type { ThreadMessage } from "../../types";
import { pluginEnabled, pluginIDs, type PluginSet } from "../registry";

type UnsubscribeActionProps = {
  item: ThreadMessage;
  plugins: PluginSet;
  busy: boolean;
  sentLabel: string;
  onRequest: (item: ThreadMessage) => void;
};

function enabled(item: ThreadMessage, plugins: PluginSet) {
  return pluginEnabled(plugins, pluginIDs.oneClickUnsubscribe) && item.one_click_unsubscribe;
}

/** OneClickUnsubscribeInlineAction renders the compact unsubscribe affordance beside sender details. */
export function OneClickUnsubscribeInlineAction({
  item,
  plugins,
  busy,
  sentLabel,
  onRequest
}: UnsubscribeActionProps) {
  if (!enabled(item, plugins)) return null;
  return (
    <button
      className={`unsubscribe-action ${sentLabel ? "sent" : ""}`}
      type="button"
      disabled={busy || Boolean(sentLabel)}
      title={sentLabel || "Unsubscribe"}
      onClick={(event) => {
        event.stopPropagation();
        onRequest(item);
      }}
    >
      {busy ? "Unsubscribing" : sentLabel || "Unsubscribe"}
    </button>
  );
}

/** OneClickUnsubscribeMenuAction renders the unsubscribe command inside the per-message action menu. */
export function OneClickUnsubscribeMenuAction({
  item,
  plugins,
  busy,
  sentLabel,
  onRequest
}: UnsubscribeActionProps) {
  if (!enabled(item, plugins)) return null;
  return (
    <button type="button" disabled={busy || Boolean(sentLabel)} onClick={() => onRequest(item)}>
      <Icon name="close" />
      {sentLabel || "Unsubscribe"}
    </button>
  );
}
