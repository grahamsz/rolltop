// File overview: Advisory spam-risk badges, evidence/feedback controls, and
// account settings for the experimental spam-filter runtime plugin.

import { Fragment, useCallback, useEffect, useState } from "react";
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import { Icon } from "../../../frontend/src/components/Icon";
import { SettingsError, SettingsLoading, SettingsPage } from "../../../frontend/src/features/settings/SettingsUI";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { DatePrefs, Toast } from "../../../frontend/src/appTypes";
import type { AccountSettingsRuntimePlugin } from "../../../frontend/src/plugins/runtime";
import type { Mailbox, Message, MessageAnnotation, ThreadMessage, User } from "../../../frontend/src/types";
import "./styles.css";

const apiBase = "/api/plugins/experimental_spam_filter";
const panelID = "experimental-spam-filter-details";
const feedbackEvent = "rolltop:experimental-spam-filter-feedback";
const bayesMinimumMessages = 200;

type SignalEvidence = {
  feature: string;
  description?: string;
  contribution: number;
};

type NeighborEvidence = {
  message_id: number;
  label?: string;
  score: number;
  weighted_coverage: number;
  date?: number;
  from?: string;
  matched_terms?: string[];
};

type Classification = {
  message_id: number;
  model_version: string;
  model_name?: string;
  training_corpus?: string;
  base_probability: number;
  labeled_neighbor_probability: number;
  labeled_neighbor_count: number;
  recent_read_support: number;
  final_probability: number;
  risk_band: "low" | "medium" | "high" | string;
  display_band: "low" | "medium" | "high" | string;
  content_coverage: string;
  feedback?: string;
  stale: boolean;
  classified_at: number;
  explanation: {
    positive_signals?: SignalEvidence[];
    negative_signals?: SignalEvidence[];
    labeled_neighbors?: NeighborEvidence[];
    recent_read_neighbors?: NeighborEvidence[];
    personal_bayes?: {
      ready: boolean;
      probability: number;
      spam_messages: number;
      ham_messages: number;
      tokens_used: number;
      bucket: string;
      log_odds_adjustment: number;
    };
    generic_read_support?: number;
    exact_sender_template_support?: number;
    reputation_log_odds_adjustment?: number;
  };
};

type Backfill = {
  model_version: string;
  status: string;
  requested: number;
  processed: number;
  failed: number;
  last_error?: string;
  started_at: number;
  updated_at: number;
  completed_at: number;
};

type Bootstrap = {
  status: string;
  cutoff_at: number;
  candidate_spam: number;
  candidate_ham: number;
  examined: number;
  accepted_spam: number;
  accepted_ham: number;
  rejected: number;
  current_mailbox?: string;
  last_error?: string;
  started_at: number;
  updated_at: number;
  completed_at: number;
};

type BootstrapSelection = {
  account_id: number;
  inbox_mailbox_id: number;
  junk_mailbox_id: number;
};

type BootstrapPreview = {
  cutoff_at: number;
  spam_candidates: number;
  ham_candidates: number;
  accounts: Array<{
    account_id: number;
    account_label: string;
    inbox_mailbox_id: number;
    inbox_name: string;
    junk_mailbox_id: number;
    junk_name: string;
    spam_candidates: number;
    ham_candidates: number;
  }>;
};

type Status = {
  model_available: boolean;
  model_version: string;
  model_name?: string;
  training_corpus?: string;
  model_error?: string;
  classified: number;
  low_risk: number;
  medium_risk: number;
  high_risk: number;
  stale: number;
  spam_feedback: number;
  ham_feedback: number;
  bayes_ready: boolean;
  bayes_spam_learned: number;
  bayes_ham_learned: number;
  bayes_explicit_spam: number;
  bayes_explicit_ham: number;
  bayes_automatic_spam: number;
  bayes_automatic_ham: number;
  backfill: Backfill;
  bootstrap: Bootstrap;
};

type SettingsContext = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
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

type AnnotationContext = {
  location: "message-list" | "thread";
  message: Message;
  annotations: MessageAnnotation[];
};

class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  const text = await response.text();
  let data: Record<string, unknown> = {};
  if (text) {
    try {
      data = JSON.parse(text) as Record<string, unknown>;
    } catch {
      data = {};
    }
  }
  if (!response.ok) {
    const message = typeof data.error === "string" ? data.error : response.statusText || "Request failed";
    throw new APIError(response.status, message);
  }
  return data as T;
}

function getStatus() {
  return requestJSON<Status>(`${apiBase}/status`);
}

function getDetail(messageID: number) {
  return requestJSON<{ classification: Classification | null; feedback: string }>(`${apiBase}/messages/${messageID}`);
}

function setMessageFeedback(csrf: string, messageID: number, label: "spam" | "ham") {
  return requestJSON<{ ok: boolean; feedback: string }>(`${apiBase}/messages/${messageID}/feedback`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify({ label })
  });
}

function clearMessageFeedback(csrf: string, messageID: number) {
  return requestJSON<{ ok: boolean; feedback: string }>(`${apiBase}/messages/${messageID}/feedback`, {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrf }
  });
}

function bootstrapRequest<T>(csrf: string, action: "preview" | "start" | "cancel" | "reset", selections?: BootstrapSelection[]) {
  return requestJSON<T>(`${apiBase}/bootstrap/${action}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify(selections ? { selections } : {})
  });
}

function messageFromError(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

function probabilityBand(value: number) {
  const bounded = Math.max(0, Math.min(1, Number(value) || 0));
  if (bounded >= .9) return "high";
  if (bounded >= .35) return "medium";
  return "low";
}

function titleCase(value: string) {
  if (!value) return "Unknown";
  return value.charAt(0).toUpperCase() + value.slice(1).replaceAll("_", " ");
}

function boundedPercent(value: number) {
  return Math.round(Math.max(0, Math.min(1, Number(value) || 0)) * 100);
}

function supportLabel(value: number) {
  const percent = boundedPercent(value);
  if (!percent) return "None";
  const strength = percent >= 60 ? "Strong" : percent >= 30 ? "Moderate" : "Weak";
  return `${strength} (${percent}%)`;
}

function signedValue(value: number) {
  const numeric = Number(value) || 0;
  return `${numeric > 0 ? "+" : ""}${numeric.toFixed(3)}`;
}

function defaultBootstrapSelections(mailboxes: Mailbox[]): BootstrapSelection[] {
  const accountIDs = Array.from(new Set(mailboxes.map((mailbox) => mailbox.account_id))).sort((a, b) => a - b);
  return accountIDs.flatMap((accountID) => {
    const accountMailboxes = mailboxes.filter((mailbox) => mailbox.account_id === accountID);
    const inbox = accountMailboxes.find((mailbox) => mailbox.role === "inbox")
      || accountMailboxes.find((mailbox) => mailbox.name.trim().toLocaleLowerCase() === "inbox");
    const junk = accountMailboxes.find((mailbox) => mailbox.role === "junk")
      || accountMailboxes.find((mailbox) => ["spam", "junk", "junk e-mail", "junk email", "[gmail]/spam", "[gmail]/junk"].includes(mailbox.name.trim().toLocaleLowerCase()));
    return inbox && junk ? [{ account_id: accountID, inbox_mailbox_id: inbox.id, junk_mailbox_id: junk.id }] : [];
  });
}

function completeBootstrapSelections(selections: BootstrapSelection[]) {
  return selections.filter((selection) => selection.inbox_mailbox_id > 0
    && selection.junk_mailbox_id > 0
    && selection.inbox_mailbox_id !== selection.junk_mailbox_id);
}

function SpamFilterSettings({ csrf, user, mailboxes, navigate, addToast }: SettingsContext) {
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState(false);
  const [limit, setLimit] = useState(500);
  const [selections, setSelections] = useState<BootstrapSelection[]>(() => defaultBootstrapSelections(mailboxes));
  const [preview, setPreview] = useState<BootstrapPreview | null>(null);

  const load = useCallback(async (quiet = false) => {
    if (!quiet) {
      setLoading(true);
      setLoadError("");
    }
    try {
      setStatus(await getStatus());
    } catch (error) {
      if (!quiet) setLoadError(messageFromError(error));
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [addToast]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (status?.backfill.status !== "running" && status?.bootstrap.status !== "running") return;
    const timer = window.setInterval(() => void load(true), 1500);
    return () => window.clearInterval(timer);
  }, [load, status?.backfill.status, status?.bootstrap.status]);

  useEffect(() => {
    setSelections((current) => current.length ? current : defaultBootstrapSelections(mailboxes));
  }, [mailboxes]);

  async function startBackfill() {
    setBusy(true);
    try {
      await requestJSON(`${apiBase}/backfill`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ limit })
      });
      addToast(`Spam-filter backfill started for up to ${limit.toLocaleString()} local messages.`);
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy(false);
    }
  }

  async function previewBootstrap() {
    setBusy(true);
    try {
      const result = await bootstrapRequest<BootstrapPreview>(csrf, "preview", completeBootstrapSelections(selections));
      setPreview(result);
      addToast(`Found ${result.spam_candidates.toLocaleString()} recent Junk and ${result.ham_candidates.toLocaleString()} read Inbox candidates.`);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy(false);
    }
  }

  async function startBootstrap() {
    setBusy(true);
    try {
      await bootstrapRequest<{ ok: boolean }>(csrf, "start", completeBootstrapSelections(selections));
      addToast("Personal spam-training bootstrap started.");
      setPreview(null);
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy(false);
    }
  }

  async function cancelBootstrap() {
    setBusy(true);
    try {
      await bootstrapRequest<{ ok: boolean }>(csrf, "cancel");
      addToast("Bootstrap cancellation requested.");
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy(false);
    }
  }

  async function resetBootstrap() {
    if (!window.confirm("Remove the inferred recent-mail snapshot? Explicit Spam and Not spam feedback will be kept.")) return;
    setBusy(true);
    try {
      await bootstrapRequest<{ ok: boolean }>(csrf, "reset");
      addToast("Automatic personal training was reset. Explicit feedback was preserved.");
      setPreview(null);
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy(false);
    }
  }

  const backfill = status?.backfill;
  const bootstrap = status?.bootstrap;
  const accountGroups = Array.from(new Set(mailboxes.map((mailbox) => mailbox.account_id))).sort((a, b) => a - b).map((accountID) => ({
    accountID,
    label: mailboxes.find((mailbox) => mailbox.account_id === accountID)?.account_label
      || mailboxes.find((mailbox) => mailbox.account_id === accountID)?.account_email
      || `Account ${accountID}`,
    mailboxes: mailboxes.filter((mailbox) => mailbox.account_id === accountID)
  }));

  function updateBootstrapMailbox(accountID: number, field: "inbox_mailbox_id" | "junk_mailbox_id", mailboxID: number) {
    setPreview(null);
    setSelections((current) => {
      const existing = current.find((item) => item.account_id === accountID) || { account_id: accountID, inbox_mailbox_id: 0, junk_mailbox_id: 0 };
      const next = { ...existing, [field]: mailboxID };
      return [...current.filter((item) => item.account_id !== accountID), next]
        .filter((item) => item.inbox_mailbox_id || item.junk_mailbox_id)
        .sort((a, b) => a.account_id - b.account_id);
    });
  }

  return (
    <SettingsPage
      title="Experimental spam filter"
      description="Advisory local scoring that never moves, hides, deletes, archives, or sends mail."
      backPath="/settings/account/plugins"
      navigate={navigate}
      className="experimental-spam-settings"
    >
      {loading ? <SettingsLoading label="Loading spam-filter status..." /> : null}
      {!loading && loadError ? <SettingsError message={loadError} onRetry={() => void load()} /> : null}
      {!loading && !loadError && status ? (
        <>
          <section className="panel experimental-spam-model-card">
            <div className="panel-headline">
              <div>
                <h2>Checked-in named-rule scorecard</h2>
                <div className="muted">{status.model_available ? `Ready · ${status.model_name || status.model_version}` : "Unavailable"}</div>
              </div>
              <span className={`experimental-spam-state ${status.model_available ? "ready" : "error"}`}>
                {status.model_available ? "Ready" : "Unavailable"}
              </span>
            </div>
            {status.model_error ? <p className="error-text">{status.model_error}</p> : null}
            {status.model_available && status.training_corpus ? <p className="muted">Mass-checked/fitted against {status.training_corpus}.</p> : null}
            <div className="experimental-spam-stats">
              <SpamStat value={status.classified} label="Classified" />
              <SpamStat value={status.high_risk} label="High risk" />
              <SpamStat value={status.medium_risk} label="Medium risk" />
              <SpamStat value={status.stale} label="Needs refresh" />
              <SpamStat value={status.spam_feedback + status.ham_feedback} label="Feedback labels" />
            </div>
          </section>

          <section className="panel experimental-spam-bayes-card">
            <div className="panel-headline">
              <div>
                <h2>Personal Bayes</h2>
                <div className="muted">Private to your Rolltop user. It learns from explicit feedback and, only when you confirm it, a reversible recent-mail snapshot.</div>
              </div>
              <span className={`experimental-spam-state ${status.bayes_ready ? "ready" : "learning"}`}>
                {status.bayes_ready ? "Ready" : "Learning"}
              </span>
            </div>
            <p>
              {status.bayes_ready
                ? "Enough labeled mail has been learned for personal Bayes to contribute to advisory scores."
                : `It will not affect scores until it has learned at least ${bayesMinimumMessages} spam and ${bayesMinimumMessages} not-spam messages.`}
            </p>
            <div className="experimental-spam-bayes-progress-list">
              <BayesProgress label="Spam learned" value={status.bayes_spam_learned} />
              <BayesProgress label="Not spam learned" value={status.bayes_ham_learned} />
            </div>
            <p className="muted">Explicit: {status.bayes_explicit_spam.toLocaleString()} spam / {status.bayes_explicit_ham.toLocaleString()} not spam · inferred snapshot: {status.bayes_automatic_spam.toLocaleString()} spam / {status.bayes_automatic_ham.toLocaleString()} not spam.</p>
          </section>

          <section className="panel experimental-spam-bootstrap-card">
            <div className="panel-headline">
              <div>
                <h2>Seed from recent mail</h2>
                <div className="muted">A confirmed, reversible snapshot from the last six months. Read-only IMAP access does not change flags, alter folders, or extend Rolltop's message cache.</div>
              </div>
              {bootstrap ? <span className={`experimental-spam-state ${bootstrap.status === "complete" ? "ready" : bootstrap.status === "failed" ? "error" : "learning"}`}>{titleCase(bootstrap.status)}</span> : null}
            </div>
            <p>Spam is inferred from the selected Junk folder. Not-spam candidates must be marked read, be at least 48 hours old, come from an established repeatedly-read sender, and stay below the independent named-rule spam threshold. Explicit feedback always wins.</p>
            <div className="experimental-spam-bootstrap-accounts">
              {accountGroups.map((group) => {
                const selected = selections.find((item) => item.account_id === group.accountID);
                return (
                  <fieldset key={group.accountID} disabled={busy || bootstrap?.status === "running"}>
                    <legend>{group.label}</legend>
                    <label><span className="settings-field-label">Inbox</span><select value={selected?.inbox_mailbox_id || 0} onChange={(event) => updateBootstrapMailbox(group.accountID, "inbox_mailbox_id", Number(event.target.value))}>
                      <option value={0}>Do not use</option>
                      {group.mailboxes.map((mailbox) => <option key={mailbox.id} value={mailbox.id} disabled={mailbox.id === selected?.junk_mailbox_id}>{mailbox.name}</option>)}
                    </select></label>
                    <label><span className="settings-field-label">Spam / Junk</span><select value={selected?.junk_mailbox_id || 0} onChange={(event) => updateBootstrapMailbox(group.accountID, "junk_mailbox_id", Number(event.target.value))}>
                      <option value={0}>Do not use</option>
                      {group.mailboxes.map((mailbox) => <option key={mailbox.id} value={mailbox.id} disabled={mailbox.id === selected?.inbox_mailbox_id}>{mailbox.name}</option>)}
                    </select></label>
                  </fieldset>
                );
              })}
              {!accountGroups.length ? <p className="muted">No IMAP mailboxes are configured.</p> : null}
            </div>
            {preview ? <div className="experimental-spam-bootstrap-preview"><strong>Ready to examine the selected folders</strong><span>{preview.spam_candidates.toLocaleString()} Junk candidates · {preview.ham_candidates.toLocaleString()} read Inbox candidates</span><small>These are broad metadata counts. Only candidates that pass the conservative policy are learned, with at most 500 unique messages per class.</small></div> : null}
            <div className="experimental-spam-bootstrap-actions">
              <button className="secondary" type="button" disabled={busy || bootstrap?.status === "running" || !completeBootstrapSelections(selections).length} onClick={() => void previewBootstrap()}><Icon name="search" />Preview sample</button>
              <button type="button" disabled={busy || bootstrap?.status === "running" || !preview} onClick={() => void startBootstrap()}><Icon name="school" />Confirm and train</button>
              {bootstrap?.status === "running" ? <button className="ghost" type="button" disabled={busy} onClick={() => void cancelBootstrap()}>Cancel</button> : null}
              <button className="ghost" type="button" disabled={busy || bootstrap?.status === "running" || (!status.bayes_automatic_spam && !status.bayes_automatic_ham)} onClick={() => void resetBootstrap()}>Reset inferred training</button>
            </div>
            {bootstrap && bootstrap.status !== "idle" ? <BootstrapStatus bootstrap={bootstrap} datePrefs={user} /> : null}
          </section>

          <section className="panel experimental-spam-backfill-card">
            <div className="panel-headline">
              <div>
                <h2>Classify local mail</h2>
                <div className="muted">Uses stored text or previews only. It does not fetch historical messages from IMAP.</div>
              </div>
            </div>
            <div className="experimental-spam-backfill-controls">
              <label>
                <span className="settings-field-label">Maximum messages</span>
                <select value={limit} onChange={(event) => setLimit(Number(event.target.value))} disabled={busy || backfill?.status === "running"}>
                  <option value={100}>100</option>
                  <option value={500}>500</option>
                  <option value={2000}>2,000</option>
                </select>
              </label>
              <button type="button" disabled={busy || !status.model_available || backfill?.status === "running"} onClick={() => void startBackfill()}>
                <Icon name="sync" />{backfill?.status === "running" ? "Backfill running" : "Start local backfill"}
              </button>
            </div>
            {backfill && backfill.status !== "idle" ? <BackfillStatus backfill={backfill} datePrefs={user} /> : null}
          </section>

          <section className="panel experimental-spam-privacy-card">
            <h2>How personalization works</h2>
            <p><strong>Spam</strong> and <strong>Not spam</strong> buttons—and successful user-initiated moves into or out of a folder Rolltop recognizes as Junk—are explicit labels. They teach personal Bayes and override every inferred snapshot label. Public corpus mail never teaches your personal Bayes classifier.</p>
            <p>Mail read within the last 90 days from the exact sender with a matching template can provide wanted-mail reputation evidence. Other similar recent reads are considered separately and carry less weight.</p>
            <p className="muted">Bootstrap bodies are fetched transiently with BODY.PEEK and discarded after tokenization. Only scores, per-user token statistics, bounded evidence, neighbor message IDs, and labels are saved; the plugin does not retain another body copy.</p>
          </section>
        </>
      ) : null}
    </SettingsPage>
  );
}

function SpamStat({ value, label }: { value: number; label: string }) {
  return <div><strong>{value.toLocaleString()}</strong><span>{label}</span></div>;
}

function BayesProgress({ label, value }: { label: string; value: number }) {
  const learned = Math.max(0, Number(value) || 0);
  const progress = Math.min(100, Math.round((learned / bayesMinimumMessages) * 100));
  return (
    <div className="experimental-spam-bayes-progress">
      <div><strong>{label}</strong><span>{learned.toLocaleString()} / {bayesMinimumMessages.toLocaleString()}</span></div>
      <div className="experimental-spam-progress" role="progressbar" aria-label={`${label}: ${learned} of ${bayesMinimumMessages}`} aria-valuemin={0} aria-valuemax={bayesMinimumMessages} aria-valuenow={Math.min(learned, bayesMinimumMessages)}>
        <span style={{ width: `${progress}%` }} />
      </div>
    </div>
  );
}

function BackfillStatus({ backfill, datePrefs }: { backfill: Backfill; datePrefs: DatePrefs }) {
  const progress = backfill.requested > 0 ? Math.min(100, Math.round((backfill.processed / backfill.requested) * 100)) : 0;
  const timestamp = backfill.completed_at || backfill.updated_at;
  return (
    <div className="experimental-spam-backfill-status" aria-live="polite">
      <div><strong>{titleCase(backfill.status)}</strong><span>{backfill.processed.toLocaleString()} processed · {backfill.failed.toLocaleString()} failed</span></div>
      <div className="experimental-spam-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}>
        <span style={{ width: `${progress}%` }} />
      </div>
      {timestamp ? <small>Updated {displayDateTime(new Date(timestamp * 1000).toISOString(), datePrefs)}</small> : null}
      {backfill.last_error ? <small className={backfill.status === "failed" ? "error-text" : "muted"}>{backfill.last_error}</small> : null}
    </div>
  );
}

function BootstrapStatus({ bootstrap, datePrefs }: { bootstrap: Bootstrap; datePrefs: DatePrefs }) {
  const candidates = Math.max(bootstrap.candidate_spam + bootstrap.candidate_ham, 0);
  const progress = candidates > 0 ? Math.min(100, Math.round((bootstrap.examined / candidates) * 100)) : 0;
  const timestamp = bootstrap.completed_at || bootstrap.updated_at;
  return (
    <div className="experimental-spam-backfill-status" aria-live="polite">
      <div><strong>{titleCase(bootstrap.status)}</strong><span>{bootstrap.accepted_spam.toLocaleString()} spam · {bootstrap.accepted_ham.toLocaleString()} not spam accepted</span></div>
      <div className="experimental-spam-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}>
        <span style={{ width: `${progress}%` }} />
      </div>
      {bootstrap.current_mailbox ? <small>Reading {bootstrap.current_mailbox}</small> : null}
      <small>{bootstrap.examined.toLocaleString()} examined · {bootstrap.rejected.toLocaleString()} rejected{timestamp ? ` · updated ${displayDateTime(new Date(timestamp * 1000).toISOString(), datePrefs)}` : ""}</small>
      {bootstrap.last_error ? <small className={bootstrap.status === "failed" ? "error-text" : "muted"}>{bootstrap.last_error}</small> : null}
    </div>
  );
}

function SpamFilterSummary({ navigate }: SettingsContext) {
  const [status, setStatus] = useState<Status | null>(null);
  useEffect(() => {
    let cancelled = false;
    void getStatus().then((next) => {
      if (!cancelled) setStatus(next);
    }).catch(() => undefined);
    return () => { cancelled = true; };
  }, []);
  return (
    <section className="panel account-list-panel">
      <div className="panel-headline">
        <div>
          <h2>Experimental spam filter</h2>
          <div className="muted">Advisory named-rule scoring with personal Bayes and tenant-local evidence.</div>
        </div>
        <button className="secondary" type="button" onClick={() => navigate("/settings/account/plugins/spam-filter")}><Icon name="report" />Review</button>
      </div>
      <button className="server-row" type="button" onClick={() => navigate("/settings/account/plugins/spam-filter")}>
        <span className="server-row-icon"><Icon name="report" /></span>
        <strong>Spam risk</strong>
        <small>{status ? `${status.classified.toLocaleString()} classified · ${status.spam_feedback + status.ham_feedback} feedback labels` : "View model and local backfill status"}</small>
      </button>
    </section>
  );
}

function SpamMenuActions({ csrf, item, activePanel, openPanel, closePanel, addToast }: MessageActionContext) {
  const [busy, setBusy] = useState("");
  const messageID = item.message.id;

  async function mutate(label: "spam" | "ham" | "") {
    setBusy(label || "clear");
    try {
      if (label) await setMessageFeedback(csrf, messageID, label);
      else await clearMessageFeedback(csrf, messageID);
      const text = label === "spam" ? "Marked as spam." : label === "ham" ? "Marked as not spam." : "Spam feedback cleared.";
      addToast(text);
      window.dispatchEvent(new CustomEvent(feedbackEvent, { detail: { messageID, label } }));
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusy("");
    }
  }

  function closeMenu(event: ReactMouseEvent<HTMLButtonElement>) {
    event.currentTarget.closest("details")?.removeAttribute("open");
  }

  return (
    <Fragment>
      <button type="button" disabled={Boolean(busy)} onClick={(event) => { closeMenu(event); void mutate("spam"); }}><Icon name="report" />{busy === "spam" ? "Saving…" : "Spam"}</button>
      <button type="button" disabled={Boolean(busy)} onClick={(event) => { closeMenu(event); void mutate("ham"); }}><Icon name="mail_open" />{busy === "ham" ? "Saving…" : "Not spam"}</button>
      <button type="button" disabled={Boolean(busy)} onClick={(event) => { closeMenu(event); void mutate(""); }}><Icon name="close" />{busy === "clear" ? "Clearing…" : "Clear spam feedback"}</button>
      <button type="button" onClick={(event) => {
        closeMenu(event);
        if (activePanel === panelID) closePanel();
        else openPanel(panelID);
      }}><Icon name="chart" />Spam filter details</button>
    </Fragment>
  );
}

function SpamDetailsPanel({ csrf, item, datePrefs, activePanel, closePanel, addToast }: MessageActionContext) {
  const open = activePanel === panelID;
  const messageID = item.message.id;
  const [classification, setClassification] = useState<Classification | null>(null);
  const [feedback, setFeedback] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await getDetail(messageID);
      setClassification(data.classification);
      setFeedback(data.feedback || data.classification?.feedback || "");
    } catch (nextError) {
      setError(messageFromError(nextError));
    } finally {
      setLoading(false);
    }
  }, [messageID]);

  useEffect(() => {
    if (open) void load();
  }, [load, open]);

  useEffect(() => {
    const listener = (event: Event) => {
      const detail = (event as CustomEvent<{ messageID?: number }>).detail;
      if (detail?.messageID === messageID && open) void load();
    };
    window.addEventListener(feedbackEvent, listener);
    return () => window.removeEventListener(feedbackEvent, listener);
  }, [load, messageID, open]);

  if (!open) return null;

  async function mutate(label: "spam" | "ham" | "") {
    setBusy(label || "clear");
    try {
      if (label) await setMessageFeedback(csrf, messageID, label);
      else await clearMessageFeedback(csrf, messageID);
      setFeedback(label);
      addToast(label === "spam" ? "Marked as spam." : label === "ham" ? "Marked as not spam." : "Spam feedback cleared.");
      window.dispatchEvent(new CustomEvent(feedbackEvent, { detail: { messageID, label } }));
    } catch (nextError) {
      addToast(messageFromError(nextError), "error");
    } finally {
      setBusy("");
    }
  }

  const displayBand = feedback === "spam" ? "high" : feedback === "ham" ? "low" : classification?.display_band || classification?.risk_band || "";
  return (
    <section className="search-explanation experimental-spam-detail" aria-live="polite">
      <div className="search-explanation-head">
        <div><strong>Experimental spam filter</strong><span>Advisory only</span></div>
        <button className="ghost search-explanation-close" type="button" title="Close" aria-label="Close spam filter details" onClick={closePanel}><Icon name="close" /></button>
      </div>
      {loading ? <p>Loading spam evidence…</p> : null}
      {error ? <p className="error-text">{error}</p> : null}
      {!loading && !error && !classification ? (
        <div className="experimental-spam-detail-body">
          {feedback ? (
            <div className="experimental-spam-scoreline">
              <SpamBadge
                band={feedback === "spam" ? "high" : "low"}
                label={feedback === "spam" ? "Marked spam" : "Marked not spam"}
                summary="Your explicit feedback; no model score is available for this message yet."
              />
              <span className="experimental-spam-feedback-label">This message has not been classified. Your feedback still teaches personal Bayes and labeled similarity for future mail.</span>
            </div>
          ) : <p>This message has not been classified. A local backfill can classify stored messages.</p>}
        </div>
      ) : null}
      {!loading && !error && classification ? (
        <div className="experimental-spam-detail-body">
          {classification.stale ? (
            <p className="experimental-spam-stale">
              The checked-in named-rule scorecard changed, so this older risk score and its evidence are hidden until a local backfill refreshes them.
              {feedback ? ` Your feedback remains ${feedback === "ham" ? "not spam" : "spam"}.` : ""}
            </p>
          ) : (
            <>
              <div className="experimental-spam-scoreline">
                <SpamBadge band={displayBand} label={`${titleCase(displayBand)} spam risk`} summary="Advisory score from named rules, ready personal Bayes, and your local evidence." />
                {feedback ? <span className="experimental-spam-feedback-label">Your feedback: {feedback === "ham" ? "not spam" : "spam"}</span> : null}
              </div>
              <ClassificationEvidence classification={classification} />
              <PersonalBayesDetails bayes={classification.explanation.personal_bayes} />
              <ReputationDetails
                exactSenderSupport={classification.explanation.exact_sender_template_support || 0}
                genericReadSupport={classification.explanation.generic_read_support ?? classification.recent_read_support}
                logOddsAdjustment={classification.explanation.reputation_log_odds_adjustment || 0}
              />
              <SignalList title="Named rules raising risk" signals={classification.explanation.positive_signals || []} />
              <SignalList title="Named rules lowering risk" signals={classification.explanation.negative_signals || []} />
              <NeighborList title="Explicitly labeled similar messages" neighbors={classification.explanation.labeled_neighbors || []} datePrefs={datePrefs} />
              <NeighborList title="Recent read messages considered" neighbors={classification.explanation.recent_read_neighbors || []} datePrefs={datePrefs} />
              <div className="experimental-spam-detail-meta">{classification.model_name || "Rolltop named-rule scorecard"}{classification.training_corpus ? ` · mass-checked/fitted against ${classification.training_corpus}` : ""} · version {classification.model_version} · {titleCase(classification.content_coverage)} content · classified {displayDateTime(new Date(classification.classified_at * 1000).toISOString(), datePrefs)}</div>
            </>
          )}
        </div>
      ) : null}
      <div className="experimental-spam-feedback-actions">
        <button type="button" disabled={Boolean(busy)} onClick={() => void mutate("spam")}><Icon name="report" />Spam</button>
        <button className="secondary" type="button" disabled={Boolean(busy)} onClick={() => void mutate("ham")}><Icon name="mail_open" />Not spam</button>
        <button className="ghost" type="button" disabled={Boolean(busy) || !feedback} onClick={() => void mutate("")}>Clear feedback</button>
      </div>
    </section>
  );
}

function ClassificationEvidence({ classification }: { classification: Classification }) {
  const bayes = classification.explanation.personal_bayes;
  const genericReadSupport = classification.explanation.generic_read_support ?? classification.recent_read_support;
  const exactSenderSupport = classification.explanation.exact_sender_template_support || 0;
  return (
    <div className="experimental-spam-evidence-grid">
      <EvidenceMetric label="Named-rule scorecard" value={`${titleCase(probabilityBand(classification.base_probability))} risk`} />
      <EvidenceMetric label="Personal Bayes" value={bayes?.ready ? `${boundedPercent(bayes.probability)}% spam · ${titleCase(bayes.bucket)}` : "Learning; not applied"} />
      <EvidenceMetric label="Labeled neighbors" value={classification.labeled_neighbor_count ? `${classification.labeled_neighbor_count} similar labels` : "None"} />
      <EvidenceMetric label="Exact-sender template" value={supportLabel(exactSenderSupport)} />
      <EvidenceMetric label="Generic recent-read overlap" value={supportLabel(genericReadSupport)} />
    </div>
  );
}

function PersonalBayesDetails({ bayes }: { bayes?: Classification["explanation"]["personal_bayes"] }) {
  if (!bayes) return null;
  if (!bayes.ready) {
    return (
      <div className="experimental-spam-evidence-note">
        <strong>Personal Bayes is still learning</strong>
        <p>{bayes.spam_messages.toLocaleString()} of {bayesMinimumMessages} spam and {bayes.ham_messages.toLocaleString()} of {bayesMinimumMessages} not-spam messages learned. It did not affect this score.</p>
      </div>
    );
  }
  return (
    <div className="experimental-spam-evidence-note">
      <strong>Personal Bayes</strong>
      <p>{boundedPercent(bayes.probability)}% spam probability · {titleCase(bayes.bucket)} bucket · {bayes.tokens_used.toLocaleString()} informative tokens · {signedValue(bayes.log_odds_adjustment)} log-odds adjustment.</p>
    </div>
  );
}

function ReputationDetails({ exactSenderSupport, genericReadSupport, logOddsAdjustment }: { exactSenderSupport: number; genericReadSupport: number; logOddsAdjustment: number }) {
  if (!exactSenderSupport && !genericReadSupport && !logOddsAdjustment) return null;
  const adjustment = Math.abs(Number(logOddsAdjustment) || 0).toFixed(3);
  const adjustmentText = logOddsAdjustment < 0
    ? `lowered the score by ${adjustment} log odds`
    : logOddsAdjustment > 0
      ? `raised the score by ${adjustment} log odds`
      : "did not change the score";
  return (
    <div className="experimental-spam-evidence-note">
      <strong>Recent-read reputation</strong>
      <p>Exact-sender template support: {supportLabel(exactSenderSupport)}. Generic recent-read overlap: {supportLabel(genericReadSupport)}. Together they {adjustmentText}.</p>
    </div>
  );
}

function EvidenceMetric({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function SignalList({ title, signals }: { title: string; signals: SignalEvidence[] }) {
  if (!signals.length) return null;
  return (
    <div className="experimental-spam-evidence-list">
      <strong>{title}</strong>
      <ul>{signals.map((signal, index) => <li key={`${signal.feature}-${index}`}><span title={signal.feature}>{signal.description || titleCase(signal.feature)}</span><span>{signal.contribution > 0 ? "+" : ""}{signal.contribution.toFixed(3)}</span></li>)}</ul>
    </div>
  );
}

function NeighborList({ title, neighbors, datePrefs }: { title: string; neighbors: NeighborEvidence[]; datePrefs: DatePrefs }) {
  if (!neighbors.length) return null;
  return (
    <div className="experimental-spam-evidence-list">
      <strong>{title}</strong>
      <ul>{neighbors.map((neighbor) => (
        <li key={`${title}-${neighbor.message_id}`}>
          <span>{neighbor.from || `Message ${neighbor.message_id}`}{neighbor.label ? ` · ${neighbor.label === "ham" ? "not spam" : neighbor.label}` : ""}</span>
          <small>{neighbor.weighted_coverage >= .6 ? "Strong" : "Moderate"} term overlap{neighbor.date ? ` · ${displayDateTime(new Date(neighbor.date * 1000).toISOString(), datePrefs)}` : ""}</small>
        </li>
      ))}</ul>
    </div>
  );
}

function SpamBadge({ band, label, summary }: { band: string; label: string; summary: string }) {
  const safeBand = band === "high" || band === "medium" || band === "low" ? band : "medium";
  return <span className={`experimental-spam-badge ${safeBand}`} title={summary} aria-label={`${label}. ${summary}`}><Icon name={safeBand === "high" ? "report" : "shield"} />{label}</span>;
}

function renderMessageAnnotations({ location, annotations }: AnnotationContext): ReactNode {
  const riskAnnotations = annotations.filter((annotation) => annotation.plugin_id === "experimental_spam_filter" && annotation.kind === "spam-risk");
  return riskAnnotations.map((annotation) => {
    if (location === "message-list" && annotation.level !== "medium" && annotation.level !== "high") return null;
    return <SpamBadge key={`${annotation.kind}-${annotation.level}`} band={annotation.level} label={annotation.label} summary={annotation.summary} />;
  });
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/plugins/spam-filter",
      aliases: ["/settings/account/experimental-spam-filter"],
      title: "Experimental spam filter",
      label: "Spam filter",
      description: "Review advisory scoring and personal Bayes training.",
      icon: "report",
      section: "plugins",
      render: (context: SettingsContext) => <SpamFilterSettings {...context} />
    }
  ],
  renderAccountSettingsSummary: (context: SettingsContext) => <SpamFilterSummary {...context} />,
  renderMessageAnnotations,
  renderMessageMenuActions: (context: MessageActionContext) => <SpamMenuActions {...context} />,
  renderMessageActionPanels: (context: MessageActionContext) => <SpamDetailsPanel {...context} />
} satisfies AccountSettingsRuntimePlugin;
