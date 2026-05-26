// File overview: Conversation detail view. It loads thread bodies, shows IMAP/blob fetch status,
// renders message headers/actions, handles inline replies, image trust, and search highlights.

import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent, ReactNode } from "react";
import { Star } from "@phosphor-icons/react";
import { api } from "../../api";
import type { DatePrefs, LocationState, Toast } from "../../appTypes";
import type { Bootstrap, ComposeForm, ComposeIdentity, HeaderDetail, Mailbox, MessageOriginalSource, ScoreExplanationNode, SearchExplanation, ThreadMessage } from "../../types";
import { Icon } from "../../components/Icon";
import { messageFromError } from "../../lib/errors";
import { displayDateTime, displayTime } from "../../lib/format";
import { HighlightedText, highlightEmailDocument } from "../../lib/searchHighlight";
import { messageBackURL, messageHighlightQuery, messageHighlightTerms } from "../../lib/routes";
import { ComposeBox } from "../compose/ComposeViews";
import { AttachmentPreviewSlot } from "../../plugins/attachmentPreview";
import { brandDomainKeyForThread, loadBrandIconsForDomains } from "../../plugins/bimiBrandIcons";
import { OneClickUnsubscribeInlineAction, OneClickUnsubscribeMenuAction } from "../../plugins/oneClickUnsubscribe";
import { RemoteImageNotice } from "../../plugins/remoteImageBlocklist/RemoteImageNotice";
import { createPluginSet } from "../../plugins/registry";
import { senderVisualURL } from "../../plugins/senderVisuals";
import { TrustImageSourceAction } from "../../plugins/trustedImageSources/TrustImageSourceAction";

type MessageLoadStatus = {
  conversation: number;
  imap_fetch_count: number;
  local_blob_count: number;
  indexed_count: number;
  unavailable_count: number;
  source: string;
};

type SearchExplanationState = {
  open: boolean;
  loading: boolean;
  error: string;
  data: SearchExplanation | null;
};

type OriginalSourceState = {
  messageID: number;
  loading: boolean;
  error: string;
  data: MessageOriginalSource | null;
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

// MessageDetailsToggle keeps the Gmail-style compact recipient line but exposes
// full message headers without leaving the conversation view.
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

/**
 * ThreadView loads a full conversation, requests status before slow IMAP/blob
 * hydration, renders plugin actions, maintains expanded/collapsed cards, and
 * prepares inline replies with the correct identity and recipient.
 */
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
  const [originalSource, setOriginalSource] = useState<OriginalSourceState | null>(null);
  const [searchExplanations, setSearchExplanations] = useState<Record<number, SearchExplanationState>>({});
  const [loadStatus, setLoadStatus] = useState<MessageLoadStatus | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const pluginKey = enabledPlugins.join("|");
  const pluginSet = useMemo(() => createPluginSet(enabledPlugins), [pluginKey]);
  const mailbox = mailboxID ? mailboxes.find((item) => item.id === mailboxID) : null;
  const trashMailbox = mailboxes.find((item) => item.role === "trash");
  const backURL = messageBackURL(location);
  const composeInitial = (composeFrom.match(/[A-Za-z0-9]/)?.[0] || "M").toUpperCase();
  const canExplainSearch = highlightQuery.trim() !== "";
  const brandDomainKey = useMemo(() => brandDomainKeyForThread(thread, pluginSet), [thread, pluginSet]);
  const [brandIcons, setBrandIcons] = useState<Record<string, string>>({});

  // Loading is split into a quick status probe and the actual message request.
  // The status dialog is delayed slightly to avoid flashing for local conversations.
  const load = useCallback(
    async (images: boolean) => {
      setLoading(true);
      setError("");
      setLoadStatus(null);
      setOriginalSource(null);
      setSearchExplanations({});
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

  async function viewOriginal(event: MouseEvent<HTMLButtonElement>, item: ThreadMessage) {
    event.stopPropagation();
    event.currentTarget.closest("details")?.removeAttribute("open");
    const messageID = item.message.id;
    setOriginalSource({ messageID, loading: true, error: "", data: null });
    try {
      const data = await api.messageOriginal(messageID);
      setOriginalSource({ messageID, loading: false, error: "", data });
    } catch (err) {
      setOriginalSource({ messageID, loading: false, error: messageFromError(err), data: null });
    }
  }

  async function moveToTrash(event: MouseEvent<HTMLButtonElement>, item: ThreadMessage) {
    event.stopPropagation();
    event.currentTarget.closest("details")?.removeAttribute("open");
    if (!trashMailbox || item.message.mailbox_id === trashMailbox.id) return;
    try {
      await api.moveMessage(csrf, item.message.id, trashMailbox.id);
      addToast(`Moved message to ${trashMailbox.name}.`);
      await refreshChrome();
      navigate(backURL);
    } catch (err) {
      addToast(`Move to trash failed: ${messageFromError(err)}`, "error");
    }
  }

  function requestUnsubscribe(item: ThreadMessage) {
    if (item.one_click_unsubscribe_sent_at) return;
    setPendingUnsubscribe(item);
  }

  async function toggleSearchExplanation(event: MouseEvent<HTMLButtonElement>, item: ThreadMessage) {
    event.stopPropagation();
    event.currentTarget.closest("details")?.removeAttribute("open");
    const messageID = item.message.id;
    const query = highlightQuery.trim();
    if (!query) return;

    const current = searchExplanations[messageID];
    if (current?.open) {
      setSearchExplanations((items) => ({
        ...items,
        [messageID]: { ...current, open: false }
      }));
      return;
    }
    if (current?.data) {
      setSearchExplanations((items) => ({
        ...items,
        [messageID]: { ...current, open: true, error: "", loading: false }
      }));
      return;
    }

    setSearchExplanations((items) => ({
      ...items,
      [messageID]: { open: true, loading: true, error: "", data: null }
    }));
    try {
      const data = await api.searchExplanation(messageID, query);
      setSearchExplanations((items) => ({
        ...items,
        [messageID]: { open: true, loading: false, error: "", data }
      }));
    } catch (err) {
      setSearchExplanations((items) => ({
        ...items,
        [messageID]: { open: true, loading: false, error: messageFromError(err), data: null }
      }));
    }
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

  // Reply setup asks the backend for recipients, identity, threading headers, and
  // reusable attachment metadata so reply/reply-all behavior stays consistent.
  async function beginReply(item: ThreadMessage, replyAll = false) {
    setExpanded((current) => new Set(current).add(item.message.id));
    try {
      const data = await api.compose(`${replyAll ? "reply_all" : "reply"}=${item.message.id}`);
      setComposeFrom(data.compose_from);
      setFromIdentities(data.from_identities || []);
      setInlineReply(data.compose);
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
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
      {originalSource ? (
        <div className="original-source-backdrop" role="presentation" onClick={() => setOriginalSource(null)}>
          <section
            className="original-source-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="original-source-title"
            onClick={(event) => event.stopPropagation()}
          >
            <header>
              <div>
                <h2 id="original-source-title">Original source</h2>
                {originalSource.data?.filename ? <span>{originalSource.data.filename}</span> : null}
              </div>
              <button className="ghost" type="button" title="Close original source" onClick={() => setOriginalSource(null)}>
                <Icon name="close" />
              </button>
            </header>
            {originalSource.loading ? <div className="original-source-status">Loading raw message source...</div> : null}
            {originalSource.error ? <div className="original-source-status error-text">{originalSource.error}</div> : null}
            {originalSource.data ? <pre tabIndex={0}>{originalSource.data.source}</pre> : null}
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
                        <button type="button" onClick={() => void beginReply(item)}>
                          <Icon name="reply" />
                          Reply
                        </button>
                        {item.can_reply_all ? (
                          <button type="button" onClick={() => void beginReply(item, true)}>
                            <Icon name="reply_all" />
                            Reply all
                          </button>
                        ) : null}
                        <button type="button" onClick={() => openCompose(`forward=${item.message.id}`)}>
                          <Icon name="forward" />
                          Forward
                        </button>
                        <button type="button" onClick={(event) => void viewOriginal(event, item)}>
                          <Icon name="file_text" />
                          View original
                        </button>
                        {trashMailbox && item.message.mailbox_id !== trashMailbox.id ? (
                          <button type="button" onClick={(event) => void moveToTrash(event, item)}>
                            <Icon name="delete" />
                            Move to trash
                          </button>
                        ) : null}
                        {canExplainSearch ? (
                          <button type="button" onClick={(event) => void toggleSearchExplanation(event, item)}>
                            <Icon name="search" />
                            Why this matched
                          </button>
                        ) : null}
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
                {searchExplanations[item.message.id]?.open ? (
                  <SearchExplanationPanel state={searchExplanations[item.message.id]} />
                ) : null}
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
                    <button className="thread-action" type="button" onClick={() => void beginReply(item)}>
                      <Icon name="reply" weight="bold" />
                      Reply
                    </button>
                    {item.can_reply_all ? (
                      <button className="thread-action" type="button" onClick={() => void beginReply(item, true)}>
                        <Icon name="reply_all" weight="bold" />
                        Reply all
                      </button>
                    ) : null}
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


function SearchExplanationPanel({ state }: { state: SearchExplanationState }) {
  const data = state.data;
  return (
    <section className="search-explanation" aria-live="polite">
      <div className="search-explanation-head">
        <strong>Why this matched</strong>
        {data?.score !== undefined ? <span>Score {formatSearchScore(data.score)}</span> : null}
      </div>
      {state.loading ? <p>Loading scoring details...</p> : null}
      {state.error ? <p className="error-text">{state.error}</p> : null}
      {!state.loading && !state.error && data && !data.matched ? (
        <p>{data.reason || "This message did not match the current search."}</p>
      ) : null}
      {!state.loading && !state.error && data?.matched ? (
        <>
          <div className="search-explanation-grid">
            <div>
              <span className="search-explanation-label">Query</span>
              <p>{data.query}</p>
            </div>
            <div>
              <span className="search-explanation-label">Matched Terms</span>
              <p>{data.terms && data.terms.length > 0 ? data.terms.join(", ") : "No highlightable text terms reported"}</p>
            </div>
          </div>
          {data.field_matches && data.field_matches.length > 0 ? (
            <div className="search-explanation-section">
              <span className="search-explanation-label">Fields</span>
              <div className="search-explanation-chips">
                {data.field_matches.map((match) => (
                  <span className="search-explanation-chip" key={match.field}>
                    {searchFieldLabel(match.field)}{match.terms.length > 0 ? `: ${match.terms.join(", ")}` : ""}
                  </span>
                ))}
              </div>
            </div>
          ) : null}
          {data.boosts && data.boosts.length > 0 ? (
            <div className="search-explanation-section">
              <span className="search-explanation-label">Ranking Boosts</span>
              <div className="search-explanation-boosts">
                {data.boosts.map((boost) => (
                  <div className="search-explanation-boost" key={`${boost.kind}:${boost.label}`}>
                    <strong>{boost.label}</strong>
                    <span>{boost.description}</span>
                    {boost.boost !== undefined ? <code>boost {formatSearchScore(boost.boost)}</code> : null}
                  </div>
                ))}
              </div>
            </div>
          ) : null}
          {data.raw ? (
            <details className="search-explanation-raw">
              <summary>Scoring detail</summary>
              <ScoreExplanationTree node={data.raw} />
            </details>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function ScoreExplanationTree({ node }: { node: ScoreExplanationNode }) {
  return (
    <div className="score-node">
      <div>
        {node.value !== undefined ? <code>{formatSearchScore(node.value)}</code> : null}
        <span>{node.message || "scorer node"}</span>
      </div>
      {node.children && node.children.length > 0 ? (
        <ul>
          {node.children.map((child, index) => (
            <li key={`${child.message}:${index}`}>
              <ScoreExplanationTree node={child} />
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function searchFieldLabel(field: string): string {
  switch (field) {
    case "subject":
    case "subject_compound":
      return "Subject";
    case "from":
    case "from_compound":
    case "from_domain":
      return "Sender";
    case "to":
      return "To";
    case "cc":
      return "Cc";
    case "body":
      return "Body";
    case "attachment_names":
      return "Attachment name";
    case "attachment_types":
      return "Attachment type";
    case "attachments":
      return "Attachment text";
    case "compound":
      return "Joined words";
    case "message_id":
      return "Message ID";
    default:
      return field.replace(/_/g, " ");
  }
}

function formatSearchScore(value: number): string {
  if (!Number.isFinite(value)) return "0";
  if (Math.abs(value) >= 1000) return value.toLocaleString(undefined, { maximumFractionDigits: 0 });
  if (Math.abs(value) >= 10) return value.toLocaleString(undefined, { maximumFractionDigits: 1 });
  return value.toLocaleString(undefined, { maximumFractionDigits: 3 });
}

// QuotedDetails lazy-renders the full body iframe only after the user asks for
// hidden quoted text, avoiding extra iframe work during initial thread paint.
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

// EmailFrame isolates message HTML in a sandboxed iframe, applies the active
// MailMirror theme, highlights search terms inside the iframe document, and
// repeatedly measures height because images/fonts can settle after load.
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
