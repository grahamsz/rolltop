// File overview: Runtime frontend settings view for the mail filters plugin.

import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import type { DatePrefs, LocationState, Toast } from "../../../frontend/src/appTypes";
import { Icon } from "../../../frontend/src/components/Icon";
import { SettingsEmpty, SettingsError, SettingsLoading, SettingsPage } from "../../../frontend/src/features/settings/SettingsUI";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { AccountSettingsRuntimePlugin } from "../../../frontend/src/plugins/runtime";
import type { Mailbox, ThreadMessage, User } from "../../../frontend/src/types";
import "./styles.css";

type Actions = {
  star: boolean;
  move_mailbox_id: number;
  move_role: string;
  forward_to: string;
};

type Rule = {
  id: number;
  name: string;
  query: string;
  enabled: boolean;
  scope_mode: "all_accounts" | "selected_accounts" | string;
  account_ids: number[];
  actions: Actions;
  position: number;
};

type Evaluation = {
  id: number;
  rule_id: number;
  message_id: number;
  phase: string;
  status: string;
  matched: boolean;
  due_at: number;
  evaluated_at: number;
  rule_name: string;
  subject: string;
  from_addr: string;
  error: string;
};

type Context = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  location: LocationState;
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

type MessageActionContext = {
  csrf: string;
  item: ThreadMessage;
  datePrefs: DatePrefs;
  activePanel: string;
  openPanel: (panelID: string) => void;
  closePanel: () => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

const blankRule: Rule = {
  id: 0,
  name: "",
  query: "",
  enabled: true,
  scope_mode: "all_accounts",
  account_ids: [],
  actions: { star: false, move_mailbox_id: 0, move_role: "", forward_to: "" },
  position: 0
};

export function MailFilterSettings({ csrf, user, mailboxes, location, navigate, addToast }: Context) {
  const [rules, setRules] = useState<Rule[]>([]);
  const [draft, setDraft] = useState<Rule>(blankRule);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState(false);

  const accounts = useMemo(() => {
    const seen = new Map<number, string>();
    mailboxes.forEach((mailbox) => {
      if (!seen.has(mailbox.account_id)) {
        seen.set(mailbox.account_id, mailbox.account_label || mailbox.account_email || `Account ${mailbox.account_id}`);
      }
    });
    return [...seen.entries()].map(([id, label]) => ({ id, label }));
  }, [mailboxes]);

  async function load(quiet = false) {
    if (!quiet) {
      setLoading(true);
      setLoadError("");
    }
    try {
      const data = await getJSON<{ rules: Rule[] }>("/api/plugins/mail_filters/rules");
      setRules(data.rules || []);
    } catch (err) {
      const message = messageFromError(err);
      if (quiet) addToast(message, "error");
      else setLoadError(message);
    } finally {
      if (!quiet) setLoading(false);
    }
  }

  useEffect(() => {
    const initialQuery = new URLSearchParams(location.search).get("query") || "";
    if (initialQuery) {
      setDraft((current) => ({ ...current, query: initialQuery, name: `Filter: ${initialQuery}` }));
    }
    void load();
  }, [location.search]);

  function edit(rule: Rule) {
    setDraft({
      ...rule,
      account_ids: rule.account_ids || [],
      actions: { ...blankRule.actions, ...(rule.actions || {}) }
    });
  }

  function resetDraft() {
    setDraft(blankRule);
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    try {
      const data = await postJSON<{ rule: Rule }>("/api/plugins/mail_filters/rules", csrf, draft);
      setDraft(data.rule);
      addToast("Filter saved.");
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setBusy(false);
    }
  }

  async function remove(rule: Rule) {
    if (!window.confirm(`Delete ${rule.name || rule.query}?`)) return;
    setBusy(true);
    try {
      await deleteJSON(`/api/plugins/mail_filters/rules/${rule.id}`, csrf);
      if (draft.id === rule.id) resetDraft();
      addToast("Filter deleted.");
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setBusy(false);
    }
  }

  async function backfill(rule: Rule) {
    if (!window.confirm(`Apply ${rule.name || rule.query} to existing mail?`)) return;
    setBusy(true);
    try {
      const data = await postJSON<{ processed: number }>(`/api/plugins/mail_filters/rules/${rule.id}/backfill`, csrf, {});
      addToast(`Backfill checked ${data.processed || 0} messages.`);
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setBusy(false);
    }
  }

  async function runDue() {
    setBusy(true);
    try {
      const data = await postJSON<{ processed: number }>("/api/plugins/mail_filters/scheduled/run", csrf, {});
      addToast(`Processed ${data.processed || 0} due scheduled filters.`);
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setBusy(false);
    }
  }

  function setAction(patch: Partial<Actions>) {
    setDraft((current) => ({ ...current, actions: { ...current.actions, ...patch } }));
  }

  function toggleAccount(id: number) {
    setDraft((current) => {
      const selected = new Set(current.account_ids || []);
      selected.has(id) ? selected.delete(id) : selected.add(id);
      return { ...current, account_ids: [...selected] };
    });
  }

  return (
    <SettingsPage
      title="Mail filters"
      description="Create search-based rules for new and existing mail."
      backPath="/settings/account/plugins"
      navigate={navigate}
      className="mail-filter-settings"
    >
      {loading ? <SettingsLoading label="Loading filters..." /> : null}
      {!loading && loadError ? <SettingsError message={loadError} onRetry={() => void load()} /> : null}
      {!loading && !loadError ? (
        <>
      <form className="panel mail-filter-editor" onSubmit={save}>
        <div className="panel-headline">
          <div>
            <h2>{draft.id ? "Edit filter" : "New filter"}</h2>
          </div>
          <button className="secondary" type="button" onClick={resetDraft}>New</button>
        </div>
        <label>
          <span className="settings-field-label">Name</span>
          <input type="text" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="Yoga reservations cleanup" />
        </label>
        <label>
          <span className="settings-field-label">Search</span>
          <input type="text" value={draft.query} onChange={(event) => setDraft({ ...draft, query: event.target.value })} placeholder='from:studio@example.com older_than:7d' required />
        </label>
        <div className="mail-filter-grid">
          <label>
            <span className="settings-field-label">Scope</span>
            <select value={draft.scope_mode} onChange={(event) => setDraft({ ...draft, scope_mode: event.target.value })}>
              <option value="all_accounts">All accounts</option>
              <option value="selected_accounts">Selected accounts</option>
            </select>
          </label>
          <label>
            <span className="settings-field-label">Move</span>
            <select value={draft.actions.move_mailbox_id || draft.actions.move_role} onChange={(event) => {
              const value = event.target.value;
              if (value === "trash") setAction({ move_role: "trash", move_mailbox_id: 0 });
              else setAction({ move_role: "", move_mailbox_id: Number(value || 0) });
            }}>
              <option value="">Do not move</option>
              <option value="trash">Source account Trash</option>
              {mailboxes.map((mailbox) => <option value={mailbox.id} key={mailbox.id}>{mailbox.account_label || mailbox.account_email} / {mailbox.name}</option>)}
            </select>
          </label>
        </div>
        {draft.scope_mode === "selected_accounts" ? (
          <div className="mail-filter-account-list">
            {accounts.map((account) => (
              <label key={account.id}>
                <input type="checkbox" checked={(draft.account_ids || []).includes(account.id)} onChange={() => toggleAccount(account.id)} />
                <span>{account.label}</span>
              </label>
            ))}
          </div>
        ) : null}
        <div className="mail-filter-actions">
          <label><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
          <label><input type="checkbox" checked={draft.actions.star} onChange={(event) => setAction({ star: event.target.checked })} /> Star matches</label>
          <label className="mail-filter-forward">
            <span className="settings-field-label">Forward to</span>
            <input type="email" value={draft.actions.forward_to} onChange={(event) => setAction({ forward_to: event.target.value })} placeholder="name@example.com" />
          </label>
        </div>
        <div className="form-actions">
          <button disabled={busy}><Icon name="label" />Save filter</button>
        </div>
      </form>
      <section className="panel">
        <div className="panel-headline">
          <div>
            <h2>Rules</h2>
            <div className="muted">{rules.length === 1 ? "1 filter" : `${rules.length} filters`}</div>
          </div>
          <button className="secondary" type="button" disabled={busy} onClick={() => void runDue()}><Icon name="clock" />Run due</button>
        </div>
        <div className="mail-filter-rule-list">
          {rules.map((rule) => (
            <div className="mail-filter-rule-row" key={rule.id}>
              <button type="button" onClick={() => edit(rule)}>
                <strong>{rule.name || rule.query}</strong>
                <small>{rule.query}</small>
              </button>
              <button className="secondary" type="button" disabled={busy} onClick={() => void backfill(rule)}><Icon name="sync" />Backfill</button>
              <button className="icon-button" type="button" disabled={busy} onClick={() => void remove(rule)} title="Delete filter"><Icon name="delete" /></button>
            </div>
          ))}
          {rules.length === 0 ? (
            <SettingsEmpty
              icon="label"
              title="No filters yet"
              description="Create a filter above to automate matching mail."
            />
          ) : null}
        </div>
      </section>
        </>
      ) : null}
    </SettingsPage>
  );
}

function MessageFilterEvaluationsPanel({ item, datePrefs, activePanel, closePanel, addToast }: MessageActionContext) {
  const [evaluations, setEvaluations] = useState<Evaluation[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const open = activePanel === "mail-filter-evaluations";

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError("");
    getJSON<{ evaluations: Evaluation[] }>(`/api/plugins/mail_filters/messages/${item.message.id}/evaluations`)
      .then((data) => {
        if (!cancelled) setEvaluations(data.evaluations || []);
      })
      .catch((err) => {
        const message = messageFromError(err);
        if (!cancelled) setError(message);
        addToast(message, "error");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [addToast, item.message.id, open]);

  if (!open) return null;
  return (
    <section className="search-explanation mail-filter-message-panel" aria-live="polite">
      <div className="search-explanation-head mail-filter-message-panel-head">
        <div>
          <strong>Filter evaluations</strong>
          <span>{evaluations.length === 1 ? "1 evaluation" : `${evaluations.length} evaluations`}</span>
        </div>
        <button className="ghost search-explanation-close" type="button" title="Close" aria-label="Close filter evaluations" onClick={closePanel}>
          <Icon name="close" />
        </button>
      </div>
      {loading ? <p>Loading filter evaluations...</p> : null}
      {error ? <p className="error-text">{error}</p> : null}
      {!loading && !error && evaluations.length === 0 ? <p>No filters have evaluated this message yet.</p> : null}
      {!loading && !error && evaluations.length > 0 ? (
        <div className="mail-filter-message-evaluations">
          {evaluations.map((ev) => (
            <div className="mail-filter-message-evaluation" key={ev.id}>
              <span className={`mail-filter-status ${ev.status}`}>{statusLabel(ev.status)}</span>
              <div>
                <strong>{ev.rule_name}</strong>
                <small>{evaluationDetail(ev, datePrefs)}</small>
                {ev.error ? <small className="error-text">{ev.error}</small> : null}
              </div>
            </div>
          ))}
        </div>
      ) : null}
    </section>
  );
}

function statusLabel(status: string) {
  return status.replaceAll("_", " ");
}

function evaluationDetail(ev: Evaluation, datePrefs: DatePrefs) {
  const parts = [
    ev.phase ? statusLabel(ev.phase) : "",
    ev.evaluated_at ? `evaluated ${displayDateTime(new Date(ev.evaluated_at * 1000).toISOString(), datePrefs)}` : "",
    ev.due_at ? `due ${displayDateTime(new Date(ev.due_at * 1000).toISOString(), datePrefs)}` : "",
    ev.matched ? "matched" : "did not match"
  ].filter(Boolean);
  return parts.join(" · ");
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { credentials: "same-origin" });
  if (!res.ok) throw new Error(await errorText(res));
  return res.json();
}

async function postJSON<T>(url: string, csrf: string, payload: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify(payload)
  });
  if (!res.ok) throw new Error(await errorText(res));
  return res.json();
}

async function deleteJSON(url: string, csrf: string) {
  const res = await fetch(url, { method: "DELETE", credentials: "same-origin", headers: { "X-CSRF-Token": csrf } });
  if (!res.ok) throw new Error(await errorText(res));
}

async function errorText(res: Response) {
  try {
    const data = await res.json();
    return data.error || data.message || res.statusText;
  } catch {
    return res.statusText;
  }
}

function messageFromError(err: unknown) {
  return err instanceof Error ? err.message : "Request failed";
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/plugins/filters",
      aliases: ["/settings/account/filters"],
      title: "Mail filters",
      label: "Filters",
      description: "Search-based rules for new and existing mail.",
      icon: "label",
      section: "plugins",
      render: (context: Context) => <MailFilterSettings {...context} />
    }
  ],
  renderAccountSettingsSummary: ({ navigate }: Context) => (
    <section className="panel account-list-panel">
      <div className="panel-headline">
        <div>
          <h2>Mail filters</h2>
          <div className="muted">Search-based automation for starring, forwarding, moving, and age-based cleanup.</div>
        </div>
        <button className="secondary" type="button" onClick={() => navigate("/settings/account/plugins/filters")}><Icon name="label" />Manage</button>
      </div>
      <button className="server-row" type="button" onClick={() => navigate("/settings/account/plugins/filters")}>
        <span className="server-row-icon"><Icon name="label" /></span>
        <strong>Filters</strong>
        <small>Create filters from searches and review the 30-day match audit.</small>
      </button>
    </section>
  ),
  renderSearchActions: ({ query, navigate }: { query: string; navigate: (url: string) => void }) => (
    <button className="secondary" type="button" onClick={() => navigate(`/settings/account/plugins/filters?query=${encodeURIComponent(query)}`)}>
      <Icon name="label" />Create filter
    </button>
  ),
  renderMessageMenuActions: ({ activePanel, openPanel, closePanel }: MessageActionContext) => (
    <button
      type="button"
      onClick={(event) => {
        event.currentTarget.closest("details")?.removeAttribute("open");
        if (activePanel === "mail-filter-evaluations") closePanel();
        else openPanel("mail-filter-evaluations");
      }}
    >
      <Icon name="rule" />
      Filter evaluations
    </button>
  ),
  renderMessageActionPanels: (context: MessageActionContext) => <MessageFilterEvaluationsPanel {...context} />
} satisfies AccountSettingsRuntimePlugin;
