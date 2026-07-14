// File overview: Advisory spam-risk badges, evidence/feedback controls, and
// account settings for the experimental spam-filter runtime plugin.

import { Fragment, useCallback, useEffect, useState } from "react";
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import { Icon } from "../../../frontend/src/components/Icon";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { DatePrefs, Toast } from "../../../frontend/src/appTypes";
import type { Mailbox, Message, MessageAnnotation, ThreadMessage, User } from "../../../frontend/src/types";
import "./styles.css";

const apiBase = "/api/plugins/experimental_spam_filter";
const panelID = "experimental-spam-filter-details";
const feedbackEvent = "rolltop:experimental-spam-filter-feedback";

type SignalEvidence = {
  feature: string;
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

type Status = {
  model_available: boolean;
  model_version: string;
  model_error?: string;
  classified: number;
  low_risk: number;
  medium_risk: number;
  high_risk: number;
  stale: number;
  spam_feedback: number;
  ham_feedback: number;
  backfill: Backfill;
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

function messageFromError(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

function probabilityBand(value: number) {
  const bounded = Math.max(0, Math.min(1, Number(value) || 0));
  if (bounded >= .8) return "high";
  if (bounded >= .35) return "medium";
  return "low";
}

function titleCase(value: string) {
  if (!value) return "Unknown";
  return value.charAt(0).toUpperCase() + value.slice(1).replaceAll("_", " ");
}

function SpamFilterSettings({ csrf, user, navigate, addToast }: SettingsContext) {
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [limit, setLimit] = useState(500);

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true);
    try {
      setStatus(await getStatus());
    } catch (error) {
      if (!quiet) addToast(messageFromError(error), "error");
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [addToast]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (status?.backfill.status !== "running") return;
    const timer = window.setInterval(() => void load(true), 1500);
    return () => window.clearInterval(timer);
  }, [load, status?.backfill.status]);

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

  const backfill = status?.backfill;
  return (
    <section className="experimental-spam-settings">
      <div className="settings-titlebar">
        <button className="icon-button" type="button" onClick={() => navigate("/settings/account")} title="Back to settings" aria-label="Back to settings">
          <Icon name="arrow_back" />
        </button>
        <div>
          <h1>Experimental spam filter</h1>
          <p>Advisory local scoring. It never moves, hides, deletes, archives, or sends mail.</p>
        </div>
      </div>

      {loading ? <section className="panel"><p>Loading spam-filter status…</p></section> : null}
      {!loading && status ? (
        <>
          <section className="panel experimental-spam-model-card">
            <div className="panel-headline">
              <div>
                <h2>Checked-in corpus model</h2>
                <div className="muted">{status.model_available ? `Ready · ${status.model_version}` : "Unavailable"}</div>
              </div>
              <span className={`experimental-spam-state ${status.model_available ? "ready" : "error"}`}>
                {status.model_available ? "Ready" : "Unavailable"}
              </span>
            </div>
            {status.model_error ? <p className="error-text">{status.model_error}</p> : null}
            <div className="experimental-spam-stats">
              <SpamStat value={status.classified} label="Classified" />
              <SpamStat value={status.high_risk} label="High risk" />
              <SpamStat value={status.medium_risk} label="Medium risk" />
              <SpamStat value={status.stale} label="Needs refresh" />
              <SpamStat value={status.spam_feedback + status.ham_feedback} label="Feedback labels" />
            </div>
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
            <p><strong>Spam</strong> and <strong>Not spam</strong> labels are strong evidence for future similar messages. Similar messages you have read and that are dated within the last 90 days provide only weak “probably wanted” evidence, capped at a 15% relative reduction.</p>
            <p className="muted">Only scores, bounded feature names, neighbor message IDs, and feedback labels are saved by this plugin. It does not save another copy of message bodies.</p>
          </section>
        </>
      ) : null}
    </section>
  );
}

function SpamStat({ value, label }: { value: number; label: string }) {
  return <div><strong>{value.toLocaleString()}</strong><span>{label}</span></div>;
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
          <div className="muted">Advisory corpus scoring with private, tenant-local similarity evidence.</div>
        </div>
        <button className="secondary" type="button" onClick={() => navigate("/settings/account/experimental-spam-filter")}><Icon name="report" />Review</button>
      </div>
      <button className="server-row" type="button" onClick={() => navigate("/settings/account/experimental-spam-filter")}>
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
              <span className="experimental-spam-feedback-label">This message has not been classified. Your feedback still trains similarity for future mail.</span>
            </div>
          ) : <p>This message has not been classified. A local backfill can classify stored messages.</p>}
        </div>
      ) : null}
      {!loading && !error && classification ? (
        <div className="experimental-spam-detail-body">
          {classification.stale ? (
            <p className="experimental-spam-stale">
              The checked-in model changed, so this older risk score and its evidence are hidden until a local backfill refreshes them.
              {feedback ? ` Your feedback remains ${feedback === "ham" ? "not spam" : "spam"}.` : ""}
            </p>
          ) : (
            <>
              <div className="experimental-spam-scoreline">
                <SpamBadge band={displayBand} label={`${titleCase(displayBand)} spam risk`} summary="Advisory score from the checked-in model and your local evidence." />
                {feedback ? <span className="experimental-spam-feedback-label">Your feedback: {feedback === "ham" ? "not spam" : "spam"}</span> : null}
              </div>
              <div className="experimental-spam-evidence-grid">
                <EvidenceMetric label="Corpus model" value={`${titleCase(probabilityBand(classification.base_probability))} risk`} />
                <EvidenceMetric label="Labeled neighbors" value={classification.labeled_neighbor_count ? `${classification.labeled_neighbor_count} similar labels` : "None"} />
                <EvidenceMetric label="Recent read support" value={classification.recent_read_support ? "Weak wanted-mail evidence" : "None"} />
              </div>
              {classification.recent_read_support > 0 ? <p className="muted">Risk was lowered by at most 15% because similar messages dated within the last 90 days are currently marked read.</p> : null}
              <SignalList title="Signals raising risk" signals={classification.explanation.positive_signals || []} />
              <SignalList title="Signals lowering risk" signals={classification.explanation.negative_signals || []} />
              <NeighborList title="Explicitly labeled similar messages" neighbors={classification.explanation.labeled_neighbors || []} datePrefs={datePrefs} />
              <NeighborList title="Similar recently read messages" neighbors={classification.explanation.recent_read_neighbors || []} datePrefs={datePrefs} />
              <div className="experimental-spam-detail-meta">Model {classification.model_version} · {titleCase(classification.content_coverage)} content · classified {displayDateTime(new Date(classification.classified_at * 1000).toISOString(), datePrefs)}</div>
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

function EvidenceMetric({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function SignalList({ title, signals }: { title: string; signals: SignalEvidence[] }) {
  if (!signals.length) return null;
  return (
    <div className="experimental-spam-evidence-list">
      <strong>{title}</strong>
      <ul>{signals.map((signal, index) => <li key={`${signal.feature}-${index}`}><code>{signal.feature}</code><span>{signal.contribution > 0 ? "+" : ""}{signal.contribution.toFixed(3)}</span></li>)}</ul>
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
      path: "/settings/account/experimental-spam-filter",
      render: (context: SettingsContext) => <SpamFilterSettings {...context} />
    }
  ],
  renderAccountSettingsSummary: (context: SettingsContext) => <SpamFilterSummary {...context} />,
  renderMessageAnnotations,
  renderMessageMenuActions: (context: MessageActionContext) => <SpamMenuActions {...context} />,
  renderMessageActionPanels: (context: MessageActionContext) => <SpamDetailsPanel {...context} />
};
