import { createElement, useEffect, useState } from "react";
import { Icon } from "../../../frontend/src/components/Icon";
import { displayDateTime } from "../../../frontend/src/lib/format";

type MailMCPGrant = {
  id: number;
  client_id: string;
  scope: string;
  redirect_uri: string;
  created_at: number;
  last_used_at: number;
};

type ToastKind = "success" | "error" | "info" | string;

type SettingsContext = {
  csrf: string;
  user: {
    date_locale?: string;
    date_format?: string;
  };
  addToast: (message: string, kind?: ToastKind) => number;
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

function MailMCPConnectedClients({ csrf, user, addToast }: SettingsContext) {
  const [grants, setGrants] = useState<MailMCPGrant[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [revokingID, setRevokingID] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    void listGrants()
      .then((data) => {
        if (cancelled) return;
        setGrants(data.grants || []);
        setLoaded(true);
      })
      .catch(() => {
        if (cancelled) return;
        setGrants([]);
        setLoaded(false);
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

  if (!loaded && grants.length === 0) return null;

  return (
    <section className="panel profile-settings">
      <div className="panel-headline profile-settings-headline">
        <div>
          <h2>Connected MCP clients</h2>
          <div className="muted">Clients listed here can read all mail mirrored in this Rolltop account.</div>
        </div>
      </div>
      {grants.length === 0 ? (
        <div className="muted">No MCP clients are currently authorized.</div>
      ) : (
        <div className="server-list">
          {grants.map((grant) => (
            <div className="server-row server-row-with-identities" key={grant.id}>
              <span>
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
      )}
    </section>
  );
}

export default {
  renderAccountSettingsSummary: (context: SettingsContext) => createElement(MailMCPConnectedClients, context)
};
