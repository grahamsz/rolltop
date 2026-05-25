import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent, ReactNode } from "react";
import { Star } from "@phosphor-icons/react";
import { api } from "../../api";
import type { DatePrefs, LocationState, Toast } from "../../appTypes";
import type { Bootstrap, ComposeForm, ComposeIdentity, HeaderDetail, Mailbox, ThreadMessage } from "../../types";
import { Icon } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";
import { displayDateTime, displayTime } from "../../lib/format";
import { HighlightedText, highlightEmailDocument } from "../../lib/searchHighlight";
import { messageBackURL, messageHighlightQuery, messageHighlightTerms } from "../../lib/routes";
import { emptyCompose } from "../../lib/composeDefaults";
import { ComposeBox } from "../compose/ComposeViews";
import { AttachmentPreviewSlot } from "../../plugins/attachmentPreview";
import { brandDomainKeyForThread, loadBrandIconsForDomains } from "../../plugins/bimiBrandIcons";
import { OneClickUnsubscribeInlineAction, OneClickUnsubscribeMenuAction } from "../../plugins/oneClickUnsubscribe";
import { RemoteImageNotice } from "../../plugins/remoteImageBlocklist/RemoteImageNotice";
import { createPluginSet } from "../../plugins/registry";
import { senderVisualURL } from "../../plugins/senderVisuals";
import { TrustImageSourceAction } from "../../plugins/trustedImageSources/TrustImageSourceAction";

type ParsedAddress = {
  label: string;
  email: string;
};

function normalizeEmail(value: string): string {
  return value.trim().toLowerCase().replace(/^mailto:/, "");
}

function extractAddresses(value: string): ParsedAddress[] {
  const input = String(value || "");
  const out: ParsedAddress[] = [];
  const seen = new Set<string>();
  const add = (label: string, email: string) => {
    const normalized = normalizeEmail(email);
    if (!normalized || seen.has(normalized)) return;
    seen.add(normalized);
    const cleanLabel = label.trim().replace(/^(to|cc|bcc)\s+/i, "");
    out.push({ label: cleanLabel || email.trim(), email: normalized });
  };
  input.replace(/([^,;<>]*<\s*([A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,})\s*>)/gi, (_match, label: string, email: string) => {
    add(label, email);
    return "";
  });
  input.replace(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi, (email) => {
    add(email, email);
    return "";
  });
  return out;
}

function firstExternalAddress(values: string[], selfEmails: Set<string>): string {
  for (const value of values) {
    for (const address of extractAddresses(value)) {
      if (!selfEmails.has(address.email)) return address.label;
    }
  }
  return "";
}

type MessageLoadStatus = {
  conversation: number;
  imap_fetch_count: number;
  local_blob_count: number;
  indexed_count: number;
  unavailable_count: number;
  source: string;
};

function shouldShowLoadStatus(status: MessageLoadStatus | null): status is MessageLoadStatus {
  return Boolean(status && (status.imap_fetch_count > 0 || status.source === "local_blob" || status.source === "local"));
}

function loadStatusTitle(status: MessageLoadStatus): string {
  if (status.imap_fetch_count > 0) {
    const count = status.imap_fetch_count;
    return `Fetching ${count} ${count === 1 ? "message" : "messages"} from IMAP server`;
  }
  if (status.source === "local_blob") {
    const count = status.local_blob_count || status.conversation;
    return `Loading ${count} ${count === 1 ? "message" : "messages"} from local blob store`;
  }
  const count = status.conversation;
  return `Loading ${count} ${count === 1 ? "message" : "messages"} from local storage`;
}

function loadStatusDetail(status: MessageLoadStatus): string {
  if (status.imap_fetch_count > 0) {
    return status.local_blob_count > 0
      ? `${status.local_blob_count} already local; waiting on the IMAP server for the rest.`
      : "Retrieving full message bodies and attachment data.";
  }
  if (status.source === "local_blob") {
    return "Everything needed for this conversation is already in the local blob store.";
  }
  return "Using local message data; no IMAP fetch is needed.";
}

function MessageDetailsToggle({
  summary,
  details,
  highlightQuery,
  highlightTerms
}: {
  summary: string;
  details: HeaderDetail[];
  highlightQuery: string;
  highlightTerms: string[];
}) {
  const visibleDetails = details.filter((detail) => detail.value.trim() !== "");
  if (visibleDetails.length === 0) {
    return (
      <div className="thread-recipients">
        <HighlightedText text={summary} query={highlightQuery} terms={highlightTerms} />
      </div>
    );
  }
  return (
    <details className="thread-recipients message-details" onClick={(event) => event.stopPropagation()}>
      <summary>
        <span>
          <HighlightedText text={summary} query={highlightQuery} terms={highlightTerms} />
        </span>
        <Icon name="expand_more" />
      </summary>
      <dl>
        {visibleDetails.map((detail) => (
          <Fragment key={`${detail.label}:${detail.value}`}>
            <dt>{detail.label}</dt>
            <dd>
              <HighlightedText text={detail.value} query={highlightQuery} terms={highlightTerms} />
            </dd>
          </Fragment>
        ))}
      </dl>
    </details>
  );
}

function SenderVisualOrAvatar({
  src,
  initial
}: {
  src: string;
  initial: string;
}) {
  const [failedSrc, setFailedSrc] = useState("");

  useEffect(() => {
    setFailedSrc("");
  }, [src]);

  if (src && failedSrc !== src) {
    return <img className="thread-brand-icon" src={src} alt="" loading="lazy" onError={() => setFailedSrc(src)} />;
  }
  return <div className="avatar">{initial}</div>;
}

type RangePagerProps = {
  page: number;
  pageSize: number;
  itemCount: number;
  total?: number;
  hasPrev: boolean;
  hasNext: boolean;
  pageURL: (page: number) => string;
  navigate: (url: string) => void;
  ariaLabel: string;
};

function ListHeader({
  title,
  titleClassName = "",
  actions,
  pager
}: {
  title: ReactNode;
  titleClassName?: string;
  actions?: ReactNode;
  pager: RangePagerProps;
}) {
  return (
    <div className="content-head list-head">
      <div className="list-head-main">
        <h1 className={titleClassName}>{title}</h1>
        {actions}
      </div>
      <RangePager {...pager} />
    </div>
  );
}

function RangePager({
  page,
  pageSize,
  itemCount,
  total,
  hasPrev,
  hasNext,
  pageURL,
  navigate,
  ariaLabel
}: RangePagerProps) {
  const start = itemCount > 0 || hasNext ? (page - 1) * pageSize + 1 : 0;
  const end = itemCount > 0 ? (page - 1) * pageSize + itemCount : start > 0 ? page * pageSize : 0;
  const cappedEnd = total && total > 0 ? Math.min(end, total) : end;
  const label = start > 0
    ? `${start.toLocaleString()}-${cappedEnd.toLocaleString()}${total && total > 0 ? ` of ${total.toLocaleString()}` : hasNext ? " of many" : ""}`
    : total && total > 0 ? `0 of ${total.toLocaleString()}` : "0";

  return (
    <div className="range-pager" aria-label={ariaLabel}>
      <span>{label}</span>
      <button className="range-pager-button" type="button" disabled={!hasPrev} onClick={() => navigate(pageURL(page - 1))} title="Previous page">
        <Icon name="chevron_left" />
      </button>
      <button className="range-pager-button" type="button" disabled={!hasNext} onClick={() => navigate(pageURL(page + 1))} title="Next page">
        <Icon name="chevron_right" />
      </button>
    </div>
  );
}

export function ThreadView({
  csrf,
  datePrefs,
  location,
  navigate,
  mailboxes,
  enabledPlugins,
  refreshChrome,
  openCompose,
  addToast
}: {
  csrf: string;
  datePrefs: DatePrefs;
  location: LocationState;
  navigate: (url: string) => void;
  mailboxes: Mailbox[];
  enabledPlugins: string[];
  refreshChrome: () => Promise<Bootstrap | null>;
  openCompose: (query?: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const id = location.path.split("/").pop() || "";
  const highlightQuery = messageHighlightQuery(location);
  const highlightTerms = messageHighlightTerms(location);
  const [thread, setThread] = useState<ThreadMessage[]>([]);
  const [subject, setSubject] = useState("");
  const [mailboxID, setMailboxID] = useState<number | null>(null);
  const [composeFrom, setComposeFrom] = useState("");
  const [fromIdentities, setFromIdentities] = useState<ComposeIdentity[]>([]);
  const [showImages, setShowImages] = useState(() => new URLSearchParams(location.search).get("images") === "1");
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set());
  const [inlineReply, setInlineReply] = useState<ComposeForm | null>(null);
  const [unsubscribingID, setUnsubscribingID] = useState<number | null>(null);
  const [pendingUnsubscribe, setPendingUnsubscribe] = useState<ThreadMessage | null>(null);
  const [loadStatus, setLoadStatus] = useState<MessageLoadStatus | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const pluginKey = enabledPlugins.join("|");
  const pluginSet = useMemo(() => createPluginSet(enabledPlugins), [pluginKey]);
  const mailbox = mailboxID ? mailboxes.find((item) => item.id === mailboxID) : null;
  const backURL = messageBackURL(location);
  const composeInitial = (composeFrom.match(/[A-Za-z0-9]/)?.[0] || "M").toUpperCase();
  const selfEmails = new Set([
    ...extractAddresses(composeFrom).map((address) => address.email),
    ...fromIdentities.map((identity) => normalizeEmail(identity.email)).filter(Boolean)
  ]);
  const isSentLikeMailbox = (mailboxID: number) => {
    const name = (mailboxes.find((item) => item.id === mailboxID)?.name || "").trim().toLowerCase();
    return name === "sent" || name.endsWith("/sent") || name.endsWith(".sent") || name.includes("[gmail]/sent");
  };
  const isOwnThreadMessage = (item: ThreadMessage) => {
    const senderAddresses = extractAddresses(`${item.sender_email} ${item.message.from_addr}`);
    return senderAddresses.some((address) => selfEmails.has(address.email)) || isSentLikeMailbox(item.message.mailbox_id);
  };
  const replyRecipientForItem = (item: ThreadMessage) => {
    if (isOwnThreadMessage(item)) {
      const recipient = firstExternalAddress([item.message.to_addr, item.message.cc_addr, item.recipient_line], selfEmails);
      if (recipient) return recipient;
      for (const candidate of [...thread].reverse()) {
        if (candidate.message.id === item.message.id || isOwnThreadMessage(candidate)) continue;
        const sender = firstExternalAddress([candidate.sender_email, candidate.message.from_addr], selfEmails);
        if (sender) return sender;
      }
    }
    return firstExternalAddress([item.sender_email, item.message.from_addr], new Set<string>()) || item.sender_email || item.message.from_addr;
  };
  const identityIDForAddresses = (values: string[]) => {
    const ids = new Map(fromIdentities.map((identity) => [normalizeEmail(identity.email), identity.id]));
    for (const value of values) {
      for (const address of extractAddresses(value)) {
        const id = ids.get(address.email);
        if (id) return id;
      }
    }
    return 0;
  };
  const replyFromIdentityIDForItem = (item: ThreadMessage) => {
    if (isOwnThreadMessage(item)) {
      return identityIDForAddresses([item.message.from_addr, item.sender_email, item.message.to_addr, item.message.cc_addr]);
    }
    return identityIDForAddresses([item.message.to_addr, item.message.cc_addr, item.recipient_line]);
  };
  const brandDomainKey = useMemo(() => brandDomainKeyForThread(thread, pluginSet), [thread, pluginSet]);
  const [brandIcons, setBrandIcons] = useState<Record<string, string>>({});

  const load = useCallback(
    async (images: boolean) => {
      setLoading(true);
      setError("");
      setLoadStatus(null);
      let statusTimer = 0;
      try {
        const status = await api.messageLoadStatus(id).catch(() => null);
        if (shouldShowLoadStatus(status)) {
          statusTimer = window.setTimeout(() => setLoadStatus(status), 250);
        }
        const data = await api.message(id, images, highlightQuery);
        setThread(data.thread);
        setSubject(data.message.subject || "(no subject)");
        setMailboxID(data.mailbox_id);
        setComposeFrom(data.compose_from);
        setFromIdentities(data.from_identities || []);
        setExpanded(new Set(data.thread.filter((item) => item.expanded).map((item) => item.message.id)));
        void refreshChrome();
      } catch (err) {
        setError(messageFromError(err));
      } finally {
        if (statusTimer) window.clearTimeout(statusTimer);
        setLoadStatus(null);
        setLoading(false);
      }
    },
    [highlightQuery, id, refreshChrome]
  );

  useEffect(() => {
    void load(showImages);
  }, [load, showImages]);

  useEffect(() => {
    if (!brandDomainKey) {
      setBrandIcons({});
      return;
    }
    let cancelled = false;
    loadBrandIconsForDomains(brandDomainKey)
      .then((data) => {
        if (!cancelled) setBrandIcons(data);
      })
      .catch(() => {
        if (!cancelled) setBrandIcons({});
      });
    return () => {
      cancelled = true;
    };
  }, [brandDomainKey]);

  async function trustImages(messageID: number) {
    try {
      await api.trustImages(csrf, messageID);
      addToast("Remote images will be shown for this sender.");
      setShowImages(true);
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function unsubscribe(item: ThreadMessage) {
    setUnsubscribingID(item.message.id);
    try {
      const data = await api.unsubscribe(csrf, item.message.id);
      const sentAt = data.sent_at || new Date().toISOString();
      setThread((current) => current.map((threadItem) =>
        threadItem.message.id === item.message.id
          ? { ...threadItem, one_click_unsubscribe_sent_at: sentAt }
          : threadItem
      ));
      addToast(data.already_sent ? `Unsubscribed on ${displayDateTime(sentAt, datePrefs)}.` : "One-click unsubscribe request sent.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setUnsubscribingID(null);
    }
  }

  async function addSender(item: ThreadMessage) {
    try {
      const data = await api.addSenderContact(csrf, item.message.id);
      addToast(data.created ? "Sender added to contacts." : "Sender is already in contacts.");
      await load(showImages);
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  function requestUnsubscribe(item: ThreadMessage) {
    if (item.one_click_unsubscribe_sent_at) return;
    setPendingUnsubscribe(item);
  }

  async function confirmUnsubscribe() {
    if (!pendingUnsubscribe) return;
    const item = pendingUnsubscribe;
    setPendingUnsubscribe(null);
    await unsubscribe(item);
  }

  function unsubscribeSentLabel(item: ThreadMessage) {
    if (!item.one_click_unsubscribe_sent_at) return "";
    return `Unsubscribed on ${displayDateTime(item.one_click_unsubscribe_sent_at, datePrefs)}`;
  }

  function beginReply(item: ThreadMessage) {
    setExpanded((current) => new Set(current).add(item.message.id));
    setInlineReply({
      ...emptyCompose,
      to: replyRecipientForItem(item),
      subject: item.reply_subject || `Re: ${item.message.subject}`,
      in_reply_to_id: item.message.id,
      from_identity_id: replyFromIdentityIDForItem(item)
    });
  }

  function toggleMessage(messageID: number) {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(messageID)) next.delete(messageID);
      else next.add(messageID);
      return next;
    });
  }

  async function toggleThreadStar(event: MouseEvent<HTMLButtonElement>, item: ThreadMessage) {
    event.stopPropagation();
    const next = !item.message.is_starred;
    setThread((current) => current.map((threadItem) => threadItem.message.id === item.message.id
      ? { ...threadItem, message: { ...threadItem.message, is_starred: next } }
      : threadItem));
    try {
      await api.setStarred(csrf, item.message.id, next);
    } catch (err) {
      setThread((current) => current.map((threadItem) => threadItem.message.id === item.message.id
        ? { ...threadItem, message: { ...threadItem.message, is_starred: item.message.is_starred } }
        : threadItem));
      addToast(`Star update failed: ${messageFromError(err)}`, "error");
    }
  }

  return (
    <>
      <div className="content-head">
        <div>
          <button className="ghost" type="button" onClick={() => navigate(backURL)} title="Back to results">
            <Icon name="arrow_back" />
          </button>
          <h1 className="thread-title">
            <HighlightedText text={subject} query={highlightQuery} terms={highlightTerms} />
          </h1>
          {mailbox ? <span className="label-pill">{mailbox.name}</span> : null}
        </div>
      </div>
      {error ? <div className="error">{error}</div> : null}
      {loading ? <div className="panel muted">Loading conversation...</div> : null}
      {loadStatus ? (
        <div className="fetch-status-backdrop" role="presentation">
          <section className="fetch-status-dialog" role="status" aria-live="polite" aria-label={loadStatusTitle(loadStatus)}>
            <h2>{loadStatusTitle(loadStatus)}</h2>
            <p>{loadStatusDetail(loadStatus)}</p>
            <div className="fetch-status-progress" aria-hidden="true">
              <span />
            </div>
          </section>
        </div>
      ) : null}
      {pendingUnsubscribe ? (
        <div className="confirm-backdrop" role="presentation" onClick={() => setPendingUnsubscribe(null)}>
          <section
            className="confirm-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="unsubscribe-confirm-title"
            onClick={(event) => event.stopPropagation()}
          >
            <h2 id="unsubscribe-confirm-title">Unsubscribe?</h2>
            <p>
              Send a one-click unsubscribe request for {pendingUnsubscribe.sender_name || pendingUnsubscribe.sender_email || "this sender"}?
            </p>
            <div className="actions">
              <button className="secondary" type="button" onClick={() => setPendingUnsubscribe(null)}>No</button>
              <button type="button" onClick={() => void confirmUnsubscribe()}>Yes, unsubscribe</button>
            </div>
          </section>
        </div>
      ) : null}
      {!loading ? (
        <section className="thread-shell">
          {thread.map((item, index) => {
            const isExpanded = expanded.has(item.message.id);
            const senderVisual = senderVisualURL(item, brandIcons, pluginSet);
            const unsubscribeSent = unsubscribeSentLabel(item);
            return (
              <article className={`thread-card ${isExpanded ? "" : "collapsed"}`} key={item.message.id}>
                <div
                  className="thread-summary"
                  role="button"
                  tabIndex={0}
                  aria-expanded={isExpanded}
                  onClick={() => toggleMessage(item.message.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      toggleMessage(item.message.id);
                    }
                  }}
                >
                  <SenderVisualOrAvatar src={senderVisual} initial={item.sender_initial} />
                  <div className="thread-person">
                    <div className="thread-from">
                      <span>
                        <HighlightedText text={item.sender_name || item.sender_email || "Unknown sender"} query={highlightQuery} terms={highlightTerms} />
                      </span>
                      <span className="thread-email">
                        <HighlightedText text={item.sender_email} query={highlightQuery} terms={highlightTerms} />
                      </span>
                      <OneClickUnsubscribeInlineAction
                        item={item}
                        plugins={pluginSet}
                        busy={unsubscribingID === item.message.id}
                        sentLabel={unsubscribeSent}
                        onRequest={requestUnsubscribe}
                      />
                    </div>
                    <MessageDetailsToggle
                      summary={item.recipient_line}
                      details={item.header_details || []}
                      highlightQuery={highlightQuery}
                      highlightTerms={highlightTerms}
                    />
                    <div className="thread-collapsed-snippet">
                      <HighlightedText text={item.snippet} query={highlightQuery} terms={highlightTerms} />
                    </div>
                  </div>
                  <div className="thread-meta">
                    <span>{displayTime(item.message.date, datePrefs)}</span>
                    <button
                      className={`star-action thread-star ${item.message.is_starred ? "starred" : ""}`}
                      type="button"
                      aria-pressed={item.message.is_starred}
                      title={item.message.is_starred ? "Unstar" : "Star"}
                      onClick={(event) => void toggleThreadStar(event, item)}
                    >
                      <Star className="icon" weight={item.message.is_starred ? "fill" : "regular"} />
                    </button>
                    <details className="message-menu" onClick={(event) => event.stopPropagation()}>
                      <summary className="icon-action" title="Message actions" aria-label="Message actions">
                        <Icon name="more_vert" />
                      </summary>
                      <div className="message-menu-panel">
                        <button type="button" onClick={() => beginReply(item)}>
                          <Icon name="reply" />
                          Reply
                        </button>
                        <button type="button" onClick={() => openCompose(`forward=${item.message.id}`)}>
                          <Icon name="forward" />
                          Forward
                        </button>
                        <button type="button" onClick={() => void addSender(item)}>
                          <Icon name="group" />
                          Add sender to contacts
                        </button>
                        <OneClickUnsubscribeMenuAction
                          item={item}
                          plugins={pluginSet}
                          busy={unsubscribingID === item.message.id}
                          sentLabel={unsubscribeSent}
                          onRequest={requestUnsubscribe}
                        />
                      </div>
                    </details>
                  </div>
                </div>
                <RemoteImageNotice
                  item={item}
                  plugins={pluginSet}
                  onShowImages={() => setShowImages(true)}
                >
                  <TrustImageSourceAction
                    item={item}
                    plugins={pluginSet}
                    onTrustImages={() => trustImages(item.message.id)}
                  />
                </RemoteImageNotice>
                <div className="thread-body">
                  {item.body_preview_only ? (
                    <div className="body-notice">
                      <Icon name="report" />
                      <span>Showing the indexed preview only. MailMirror could not fetch the full original from IMAP.</span>
                      <button className="secondary" type="button" onClick={() => navigate("/settings/account")}>Account settings</button>
                    </div>
                  ) : null}
                  <EmailFrame srcDoc={item.body_doc} highlightQuery={highlightQuery} highlightTerms={highlightTerms} />
                  {item.has_hidden_quoted && item.full_body_doc ? (
                    <QuotedDetails srcDoc={item.full_body_doc} highlightQuery={highlightQuery} highlightTerms={highlightTerms} />
                  ) : null}
                </div>
                {item.attachments.length > 0 ? (
                  <div className="attachments">
                    {item.attachments.map((attachment) => (
                      <div className={`attachment-group ${attachment.matched ? "matched" : ""}`} key={attachment.id}>
                        <a
                          className="attachment"
                          href={attachment.download_url}
                          download={attachment.filename || "attachment"}
                        >
                          <Icon name="attach_file" />
                          <HighlightedText
                            text={attachment.filename || "Attachment"}
                            query={highlightQuery}
                            terms={highlightTerms}
                          />
                        </a>
                        <AttachmentPreviewSlot attachment={attachment} plugins={pluginSet} />
                      </div>
                    ))}
                  </div>
                ) : null}
                {index === thread.length - 1 && inlineReply?.in_reply_to_id !== item.message.id ? (
                  <div className="thread-actions">
                    <button className="thread-action" type="button" onClick={() => beginReply(item)}>
                      <Icon name="reply" weight="bold" />
                      Reply
                    </button>
                    <button
                      className="thread-action"
                      type="button"
                      onClick={() => openCompose(`forward=${item.message.id}`)}
                    >
                      <Icon name="forward" weight="bold" />
                      Forward
                    </button>
                  </div>
                ) : null}
                {inlineReply && inlineReply.in_reply_to_id === item.message.id ? (
                  <div className="inline-reply-row">
                    <div className="avatar inline-reply-avatar">{composeInitial}</div>
                    <ComposeBox
                      csrf={csrf}
                      composeFrom={composeFrom}
                      identities={fromIdentities}
                      initial={inlineReply}
                      inline
                      addToast={addToast}
                      onSent={() => {
                        setInlineReply(null);
                        void load(showImages);
                      }}
                      onCancel={() => setInlineReply(null)}
                    />
                  </div>
                ) : null}
                {index === thread.length - 1 && !inlineReply ? <div className="thread-tail" /> : null}
              </article>
            );
          })}
        </section>
      ) : null}
    </>
  );
}

function QuotedDetails({
  srcDoc,
  highlightQuery,
  highlightTerms
}: {
  srcDoc: string;
  highlightQuery: string;
  highlightTerms: string[];
}) {
  const [open, setOpen] = useState(false);

  return (
    <details className="quoted-details" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>...</summary>
      {open ? <EmailFrame srcDoc={srcDoc} highlightQuery={highlightQuery} highlightTerms={highlightTerms} full /> : null}
    </details>
  );
}

function currentEmailDocumentTheme(): "classic" | "classic_dark" | "matrix" {
  const theme = document.documentElement.dataset.theme;
  return theme === "classic_dark" || theme === "matrix" ? theme : "classic";
}

function themedEmailSrcDoc(srcDoc: string): string {
  const theme = currentEmailDocumentTheme();
  if (theme === "classic") return srcDoc;
  return srcDoc.replace(/<html(\s|>)/i, `<html data-mailmirror-theme="${theme}"$1`);
}

function applyEmailDocumentTheme(doc: Document | null | undefined) {
  if (!doc) return;
  const theme = currentEmailDocumentTheme();
  if (theme === "classic") {
    doc.documentElement.removeAttribute("data-mailmirror-theme");
    return;
  }
  doc.documentElement.setAttribute("data-mailmirror-theme", theme);
}

function EmailFrame({
  srcDoc,
  highlightQuery = "",
  highlightTerms = [],
  full = false
}: {
  srcDoc: string;
  highlightQuery?: string;
  highlightTerms?: string[];
  full?: boolean;
}) {
  const ref = useRef<HTMLIFrameElement | null>(null);
  const [height, setHeight] = useState(full ? 220 : 96);
  const highlightKey = `${highlightQuery}:${highlightTerms.join(",")}`;
  const themedSrcDoc = themedEmailSrcDoc(srcDoc);

  useEffect(() => {
    setHeight(full ? 220 : 96);
  }, [srcDoc, highlightKey, full]);

  function resize() {
    const doc = ref.current?.contentDocument;
    const body = doc?.body;
    const html = doc?.documentElement;
    if (!body || !html) return;
    const next = Math.max(body.scrollHeight, body.offsetHeight, html.scrollHeight, html.offsetHeight, full ? 180 : 84) + 12;
    setHeight(next);
  }

  return (
    <iframe
      ref={ref}
      className={`email-frame ${full ? "full" : ""}`}
      srcDoc={themedSrcDoc}
      title="Email body"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      scrolling="no"
      style={{ height }}
      onLoad={() => {
        const doc = ref.current?.contentDocument;
        applyEmailDocumentTheme(doc);
        highlightEmailDocument(doc, highlightQuery, highlightTerms);
        resize();
        window.requestAnimationFrame(resize);
        window.setTimeout(resize, 120);
        window.setTimeout(resize, 600);
        window.setTimeout(resize, 1600);
      }}
    />
  );
}
