import { useCallback, useEffect, useState } from "react";
import { api } from "../../../api";
import type { AddToast } from "../../../appTypes";
import type { PluginSetting } from "../../../types";
import { messageFromError } from "../../../lib/errors";

export function PluginTogglePanel({
  csrf,
  addToast,
  onPluginsChanged,
  onPluginSaved
}: {
  csrf: string;
  addToast: AddToast;
  onPluginsChanged?: (plugins: PluginSetting[]) => void;
  onPluginSaved?: () => void | Promise<unknown>;
}) {
  const [plugins, setPlugins] = useState<PluginSetting[]>([]);
  const [loading, setLoading] = useState(true);
  const [savingPlugin, setSavingPlugin] = useState("");

  const setPluginList = useCallback(
    (nextPlugins: PluginSetting[]) => {
      setPlugins(nextPlugins);
      onPluginsChanged?.(nextPlugins);
    },
    [onPluginsChanged]
  );

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.adminPlugins();
      setPluginList(data.plugins);
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setLoading(false);
    }
  }, [addToast, setPluginList]);

  useEffect(() => {
    void load();
  }, [load]);

  async function setPlugin(plugin: PluginSetting, enabled: boolean) {
    setSavingPlugin(plugin.id);
    try {
      const data = await api.setAdminPlugin(csrf, plugin.id, enabled);
      setPluginList(data.plugins);
      addToast(`${plugin.name} ${enabled ? "enabled" : "disabled"}.`);
      await onPluginSaved?.();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSavingPlugin("");
    }
  }

  return (
    <section className="panel plugin-settings-panel">
      <h2>Plugins</h2>
      {loading ? <div className="muted">Loading plugins...</div> : null}
      <div className="plugin-list">
        {plugins.map((plugin) => (
          <label className="plugin-row" key={plugin.id}>
            <input
              type="checkbox"
              checked={plugin.enabled}
              disabled={savingPlugin === plugin.id}
              onChange={(event) => void setPlugin(plugin, event.target.checked)}
            />
            <span>
              <strong>{plugin.name}</strong>
              <small>{plugin.description}</small>
              {plugin.heavy ? <em>Lazy loaded</em> : null}
            </span>
          </label>
        ))}
      </div>
    </section>
  );
}
