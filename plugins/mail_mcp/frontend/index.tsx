import { useCallback, useEffect, useState } from "react";
import type { AddToast, DatePrefs } from "../../../frontend/src/appTypes";
import { Icon } from "../../../frontend/src/components/Icon";
import { SettingsEmpty, SettingsError, SettingsLoading, SettingsPage } from "../../../frontend/src/features/settings/SettingsUI";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { AccountSettingsRuntimePlugin } from "../../../frontend/src/plugins/runtime";

type MailMCPGrant = {
  id: number;
  client_id: string;
  scope: string;
  redirect_uri: string;
  created_at: number;
  last_used_at: number;
};

type SettingsContext = {
  csrf: string;
  user: DatePrefs;
  navigate: (url: string) => void;
  addToast: AddToast;
};

class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function parseJSON<T>(res: Response): Promise<T> {
  const text = await res.text();
  const data = text ? JSON.parse(text) as Record<string, unknown> : {};
  if (!res.ok) {
    throw new APIError(res.status, typeof data.error === "string" ? data.error : res.statusText);
  }
  return data as T;
}

async function listGrants() {
  return parseJSON<{ grants: MailMCPGrant[] }>(await fetch("/api/plugins/mail_mcp/grants", {
    headers: { Accept: "application/json" }
  }));
}

async function revokeGrant(csrf: string, id: number) {
  return parseJSON<{ ok: boolean }>(await fetch(`/api/plugins/mail_mcp/grants/${id}`, {
    method: "DELETE",
    headers: {
      Accept: "application/json",
      "X-CSRF-Token": csrf
    }
  }));
}

function messageFromError(err: unknown) {
  return err instanceof Error ? err.message : "Request failed.";
}

function clientLabel(grant: MailMCPGrant) {
  const client = grant.client_id.trim();
  if (client.toLowerCase() === "chatgpt") return "ChatGPT";
  return client || "MCP client";
}

function MailMCPConnectedClients({ csrf, user, navigate, addToast }: SettingsContext) {
  const [grants, setGrants] = useState<MailMCPGrant[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [revokingID, setRevokingID] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const data = await listGrants();
      setGrants(data.grants || []);
    } catch (err) {
      setLoadError(messageFromError(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setLoadError("");
    void listGrants().then((data) => {
      if (!cancelled) setGrants(data.grants || []);
    }).catch((err) => {
      if (!cancelled) setLoadError(messageFromError(err));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  async function revoke(grant: MailMCPGrant) {
    if (!window.confirm(`Revoke ${clientLabel(grant)} read access to all mirrored mail?`)) return;
    setRevokingID(grant.id);
    try {
      await revokeGrant(csrf, grant.id);
      setGrants((current) => current.filter((item) => item.id !== grant.id));
      addToast("Mail MCP access revoked.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setRevokingID(null);
    }
  }

  return (
    <SettingsPage
      title="Connected apps"
      description="Review and revoke apps that can read mirrored mail through Mail MCP."
      backPath="/settings/account/plugins"
      navigate={navigate}
      className="mail-mcp-settings"
    >
      {loading ? <SettingsLoading label="Loading connected apps..." /> : null}
      {!loading && loadError ? <SettingsError message={loadError} onRetry={() => void load()} /> : null}
      {!loading && !loadError && grants.length === 0 ? (
        <SettingsEmpty
          icon="link"
          title="No connected apps"
          description="Apps authorized through Mail MCP will appear here."
        />
      ) : null}
      {!loading && !loadError && grants.length > 0 ? (
        <section className="panel profile-settings">
          <div className="panel-headline profile-settings-headline">
            <div>
              <h2>Mail MCP clients</h2>
              <div className="muted">These clients can read all mail mirrored in this Rolltop account.</div>
            </div>
          </div>
          <div className="mail-mcp-client-list">
            {grants.map((grant) => (
              <div className="mail-mcp-client-row" key={grant.id}>
                <span className="mail-mcp-client-copy">
                  <strong>{clientLabel(grant)}</strong>
                  <small>Can read all mirrored mail · {grant.scope || "mail.readonly"}</small>
                  <small>{grant.last_used_at ? `Last used ${displayDateTime(new Date(grant.last_used_at * 1000).toISOString(), user)}` : `Allowed ${displayDateTime(new Date(grant.created_at * 1000).toISOString(), user)}`}</small>
                </span>
                <button className="danger secondary" type="button" disabled={revokingID === grant.id} onClick={() => void revoke(grant)}>
                  <Icon name="delete" />{revokingID === grant.id ? "Revoking..." : "Revoke"}
                </button>
              </div>
            ))}
          </div>
        </section>
      ) : null}
    </SettingsPage>
  );
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/plugins/connected-apps",
      title: "Connected apps",
      label: "Connected apps",
      description: "Manage apps authorized through Mail MCP.",
      icon: "link",
      section: "plugins",
      render: (context: SettingsContext) => <MailMCPConnectedClients {...context} />
    }
  ]
} satisfies AccountSettingsRuntimePlugin;
