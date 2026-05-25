import { useEffect, useRef, useState } from "react";
import type { ChangeEvent, KeyboardEvent, MouseEvent } from "react";
import { Star } from "@phosphor-icons/react";
import { api } from "../../api";
import type { DatePrefs, Toast, LocationState } from "../../appTypes";
import type { Bootstrap, Conversation, Mailbox, SyncRun } from "../../types";
import { Icon } from "../../components/Icon";
import { ListHeader } from "../../components/common";
import { messageFromError } from "../../lib/errors";
import { displayTime } from "../../lib/format";
import { effectiveMailboxSyncMode, mailboxActiveRun, mailboxNeedsSync, mailboxRefreshKey } from "../../lib/sync";
import { HighlightedText } from "../../lib/searchHighlight";
import { mailPageSize } from "../../lib/constants";
import { mailRoute, mailURL, messageURL, routeWithSearch, searchRoute, searchURL } from "../../lib/routes";

export function MailView({
  csrf,
  datePrefs,
  location,
  navigate,
  hiddenMessageIDs,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  refreshChrome,
  addToast
}: {
  csrf: string;
  datePrefs: DatePrefs;
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loading, setLoading] = useState(true);
  const [syncBusy, setSyncBusy] = useState(false);
  const loaded = useRef(false);
  const [error, setError] = useState("");
  const [hasPrev, setHasPrev] = useState(false);
  const [hasNext, setHasNext] = useState(false);
  const [newMessageIDs, setNewMessageIDs] = useState<Set<number>>(() => new Set());
  const previousPageIDs = useRef<Set<number>>(new Set());
  const previousListKey = useRef("");
  const newMessageTimer = useRef<number | null>(null);
  const route = mailRoute(location.path);
  const mailboxID = route.mailboxID;
  const page = route.page;
  const mailbox = mailboxes.find((item) => String(item.id) === mailboxID);
  const totalCount = mailbox ? mailbox.message_count : mailboxes.filter((item) => item.show_in_all_mail !== false).reduce((sum, item) => sum + item.message_count, 0);
  const refreshKey = mailboxRefreshKey(latestSyncRun, mailbox);
  const listKey = `${mailboxID || "all"}:${page}`;
  const listPending = loading || previousListKey.current !== listKey;
  const activeRun = mailboxActiveRun(mailbox, activeSyncRuns, latestSyncRun);
  const effectiveMode = mailbox ? effectiveMailboxSyncMode(mailbox, mailboxes) : "auto";

  useEffect(() => {
    return () => {
      if (newMessageTimer.current !== null) window.clearTimeout(newMessageTimer.current);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const isNewList = previousListKey.current !== listKey;
    const canAnimateNewMail = page === 1 && loaded.current && !isNewList && Boolean(refreshKey) && Boolean(latestSyncRun?.new_messages);
    if (isNewList || !loaded.current) {
      setLoading(true);
      setConversations([]);
      setHasPrev(false);
      setHasNext(false);
    }
    setError("");
    api
      .mail(mailboxID, page)
      .then((data) => {
        if (cancelled) return;
        const nextIDs = new Set(data.conversations.map((conversation) => conversation.message.id));
        if (canAnimateNewMail) {
          const appeared = data.conversations
            .map((conversation) => conversation.message.id)
            .filter((id) => !previousPageIDs.current.has(id));
          if (appeared.length > 0) {
            setNewMessageIDs(new Set(appeared));
            if (newMessageTimer.current !== null) window.clearTimeout(newMessageTimer.current);
            newMessageTimer.current = window.setTimeout(() => setNewMessageIDs(new Set()), 2200);
          }
        } else {
          setNewMessageIDs(new Set());
        }
        previousPageIDs.current = nextIDs;
        previousListKey.current = listKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
      })
      .catch((err) => {
        if (!cancelled) {
          previousPageIDs.current = new Set();
          previousListKey.current = listKey;
          setConversations([]);
          setHasPrev(false);
          setHasNext(false);
          setError(messageFromError(err));
        }
      })
      .finally(() => {
        if (!cancelled) {
          loaded.current = true;
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [mailboxID, page, refreshKey, listKey, latestSyncRun?.new_messages]);

  const pageURL = (nextPage: number) => mailURL(mailboxID, nextPage);

  function updateStarred(messageID: number, starredMessageID: number, starred: boolean) {
    setConversations((current) => current.map((conversation) => {
      if (conversation.message.id !== messageID && conversation.starred_message_id !== starredMessageID) return conversation;
      return {
        ...conversation,
        starred_message_id: starred ? starredMessageID : conversation.message.id,
        message: { ...conversation.message, is_starred: starred }
      };
    }));
  }

  async function startFolderSync() {
    if (!mailbox) return;
    setSyncBusy(true);
    try {
      if (effectiveMode === "never") {
        await api.setFolderMode(csrf, mailbox.id, "manual");
      }
      await api.syncFolder(csrf, mailbox.id);
      addToast(`${mailbox.name} sync started.`);
      await refreshChrome();
    } catch (err) {
      addToast(`Sync failed: ${messageFromError(err)}`, "error");
    } finally {
      setSyncBusy(false);
    }
  }

  return (
    <>
      <ListHeader
        title={mailbox?.name || "All Mail"}
        titleClassName="mailbox-title"
        pager={{
          page,
          pageSize: mailPageSize,
          itemCount: listPending ? 0 : conversations.length,
          total: totalCount,
          hasPrev: listPending ? false : hasPrev,
          hasNext: listPending ? false : hasNext,
          pageURL,
          navigate,
          ariaLabel: "Mailbox pagination"
        }}
      />
      {mailbox ? (
        <FolderSyncNotice
          mailbox={mailbox}
          effectiveMode={effectiveMode}
          activeRun={activeRun}
          busy={syncBusy}
          onSync={startFolderSync}
        />
      ) : null}
      {error ? <div className="error">{error}</div> : null}
      {listPending ? <MessageListSkeleton label="Loading messages" /> : null}
      {!listPending && !error ? (
        <MessageList
          csrf={csrf}
          conversations={conversations}
          hiddenMessageIDs={hiddenMessageIDs}
          highlightMessageIDs={newMessageIDs}
          datePrefs={datePrefs}
          returnURL={mailURL(mailboxID, page)}
          navigate={navigate}
          addToast={addToast}
          onStarredChange={updateStarred}
        />
      ) : null}
    </>
  );
}

function FolderSyncNotice({
  mailbox,
  effectiveMode,
  activeRun,
  busy,
  onSync
}: {
  mailbox: Mailbox;
  effectiveMode: string;
  activeRun: SyncRun | null;
  busy: boolean;
  onSync: () => void;
}) {
  const syncOff = effectiveMode === "never";
  const needsManualSync = effectiveMode === "manual" && mailboxNeedsSync(mailbox) && !activeRun;
  if (!syncOff && !needsManualSync) return null;

  const title = syncOff ? "Folder sync is off" : "Folder is not fully synced";
  const detail = syncOff
    ? "This folder is excluded from sync, so MailMirror may only show messages that were already mirrored."
    : "This manual-sync folder is behind the remote mailbox. Sync it to mirror the latest messages.";
  const buttonLabel = busy ? "Starting" : syncOff ? "Enable and sync" : "Sync folder";

  return (
    <section className="folder-sync-notice" aria-live="polite">
      <Icon name="report" />
      <div className="folder-sync-copy">
        <strong>{title}</strong>
        <span>{detail}</span>
      </div>
      <button className={syncOff ? "" : "secondary"} type="button" disabled={busy} onClick={onSync}>
        <Icon name="sync" />
        {buttonLabel}
      </button>
    </section>
  );
}

export function SearchView({
  csrf,
  location,
  navigate,
  hiddenMessageIDs,
  datePrefs,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  datePrefs: DatePrefs;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [hasPrev, setHasPrev] = useState(false);
  const [hasNext, setHasNext] = useState(false);
  const loadedKey = useRef("");
  const route = searchRoute(location.path);
  const query = route.query;
  const page = route.page;
  const searchKey = `${query}:best:${page}`;
  const listPending = loading || loadedKey.current !== searchKey;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setConversations([]);
    setHasPrev(false);
    setHasNext(false);
    setError("");
    api
      .search(query, "best", page)
      .then((data) => {
        if (cancelled) return;
        loadedKey.current = searchKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
      })
      .catch((err) => {
        if (!cancelled) {
          loadedKey.current = searchKey;
          setConversations([]);
          setHasPrev(false);
          setHasNext(false);
          setError(messageFromError(err));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [query, page, searchKey]);

  const pageURL = (nextPage: number) => searchURL(query, nextPage);
  const returnURL = routeWithSearch(location.path, location.search);

  function updateStarred(messageID: number, starredMessageID: number, starred: boolean) {
    setConversations((current) => current.map((conversation) => {
      if (conversation.message.id !== messageID && conversation.starred_message_id !== starredMessageID) return conversation;
      return {
        ...conversation,
        starred_message_id: starred ? starredMessageID : conversation.message.id,
        message: { ...conversation.message, is_starred: starred }
      };
    }));
  }

  return (
    <>
      <ListHeader
        title="Search"
        pager={{
          page,
          pageSize: mailPageSize,
          itemCount: listPending ? 0 : conversations.length,
          hasPrev: listPending ? false : hasPrev,
          hasNext: listPending ? false : hasNext,
          pageURL,
          navigate,
          ariaLabel: "Search pagination"
        }}
      />
      {query ? <div className="muted">Results for <strong>{query}</strong></div> : null}
      {error ? <div className="error">{error}</div> : null}
      {listPending ? <MessageListSkeleton label="Searching" /> : null}
      {!listPending && !error ? (
        <MessageList
          csrf={csrf}
          conversations={conversations}
          hiddenMessageIDs={hiddenMessageIDs}
          navigate={navigate}
          searchQuery={query}
          datePrefs={datePrefs}
          returnURL={returnURL}
          addToast={addToast}
          onStarredChange={updateStarred}
        />
      ) : null}
    </>
  );
}

function MessageListSkeleton({ label }: { label: string }) {
  return (
    <div className="message-table loading-list" role="status" aria-label={label} aria-busy="true">
      {Array.from({ length: 8 }, (_, index) => (
        <div className="message-row skeleton-row" key={index}>
          <span className="skeleton-block sender-skeleton" />
          <span className="skeleton-block subject-skeleton" />
          <span className="skeleton-block date-skeleton" />
        </div>
      ))}
    </div>
  );
}

function MessageList({
  csrf,
  conversations,
  hiddenMessageIDs,
  highlightMessageIDs,
  searchQuery = "",
  datePrefs,
  returnURL = "",
  navigate,
  addToast,
  onStarredChange
}: {
  csrf: string;
  conversations: Conversation[];
  hiddenMessageIDs: Set<number>;
  highlightMessageIDs?: Set<number>;
  searchQuery?: string;
  datePrefs: DatePrefs;
  returnURL?: string;
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onStarredChange: (messageID: number, starredMessageID: number, starred: boolean) => void;
}) {
  const [selectedIDs, setSelectedIDs] = useState<Set<number>>(() => new Set());
  const lastSelectedIndex = useRef<number | null>(null);
  const visible = conversations.filter((conversation) => !hiddenMessageIDs.has(conversation.message.id));
  const visibleKey = visible.map((conversation) => conversation.message.id).join(",");

  useEffect(() => {
    const ids = new Set(visible.map((conversation) => conversation.message.id));
    setSelectedIDs((current) => {
      const next = new Set(Array.from(current).filter((id) => ids.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [visibleKey]);

  function selectedDragIDs(messageID: number): number[] {
    if (!selectedIDs.has(messageID)) return [messageID];
    const selected = visible.map((conversation) => conversation.message.id).filter((id) => selectedIDs.has(id));
    return selected.length > 0 ? selected : [messageID];
  }

  function selectMessage(event: ChangeEvent<HTMLInputElement>, index: number, messageID: number) {
    event.stopPropagation();
    const checked = event.currentTarget.checked;
    setSelectedIDs((current) => {
      const next = new Set(current);
      if ((event.nativeEvent as Event & { shiftKey?: boolean }).shiftKey && lastSelectedIndex.current !== null) {
        const start = Math.min(lastSelectedIndex.current, index);
        const end = Math.max(lastSelectedIndex.current, index);
        for (const conversation of visible.slice(start, end + 1)) {
          if (checked) next.add(conversation.message.id);
          else next.delete(conversation.message.id);
        }
      } else if (checked) {
        next.add(messageID);
      } else {
        next.delete(messageID);
      }
      return next;
    });
    lastSelectedIndex.current = index;
  }

  async function toggleStar(event: MouseEvent<HTMLButtonElement>, conversation: Conversation) {
    event.preventDefault();
    event.stopPropagation();
    const msg = conversation.message;
    const targetID = conversation.starred_message_id || msg.id;
    const next = !msg.is_starred;
    onStarredChange(msg.id, targetID, next);
    try {
      await api.setStarred(csrf, targetID, next);
    } catch (err) {
      onStarredChange(msg.id, targetID, msg.is_starred);
      addToast(`Star update failed: ${messageFromError(err)}`, "error");
    }
  }

  function openRow(event: MouseEvent<HTMLDivElement>, href: string) {
    if ((event.target as HTMLElement).closest("button,input,label")) return;
    navigate(href);
  }

  function openRowWithKeyboard(event: KeyboardEvent<HTMLDivElement>, href: string) {
    if (event.currentTarget !== event.target) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      navigate(href);
    }
  }

  if (visible.length === 0) {
    return <div className="panel muted">No messages here.</div>;
  }
  return (
    <div className="message-table">
      {visible.map((conversation, index) => {
        const msg = conversation.message;
        const matchTerms = conversation.match_terms || [];
        const href = messageURL(msg.id, searchQuery, matchTerms, returnURL);
        const attachmentNames = conversation.attachment_names || [];
        const attachmentMatches = conversation.attachment_matches || [];
        const selected = selectedIDs.has(msg.id);
        return (
          <div
            className={`message-row ${conversation.is_read ? "read" : "unread"} ${selected ? "selected" : ""} ${highlightMessageIDs?.has(msg.id) ? "new-delivery" : ""}`}
            draggable
            key={msg.id}
            role="link"
            tabIndex={0}
            onClick={(event) => openRow(event, href)}
            onKeyDown={(event) => openRowWithKeyboard(event, href)}
            onDragStart={(event) => {
              const ids = selectedDragIDs(msg.id);
              event.dataTransfer.effectAllowed = "move";
              event.dataTransfer.setData("application/x-mailmirror-messages", JSON.stringify(ids));
              event.dataTransfer.setData("application/x-mailmirror-message", String(ids[0]));
              event.dataTransfer.setData("text/plain", String(ids[0]));
            }}
          >
            <label className="message-select" onClick={(event) => event.stopPropagation()} title="Select message">
              <input
                type="checkbox"
                checked={selected}
                aria-label={`Select ${msg.subject || "message"}`}
                onChange={(event) => selectMessage(event, index, msg.id)}
              />
            </label>
            <button
              className={`star-action ${msg.is_starred ? "starred" : ""}`}
              type="button"
              aria-pressed={msg.is_starred}
              title={msg.is_starred ? "Unstar" : "Star"}
              onClick={(event) => void toggleStar(event, conversation)}
            >
              <Star className="icon" weight={msg.is_starred ? "fill" : "regular"} />
            </button>
            <span className="sender">
              <span className="sender-name">
                <HighlightedText text={conversation.participants || msg.from_addr || "Unknown sender"} query={searchQuery} terms={matchTerms} />
              </span>
              {conversation.count > 1 ? <span className="thread-count">({conversation.count})</span> : null}
            </span>
            <span className="subject">
              <strong>
                <HighlightedText text={msg.subject || "(no subject)"} query={searchQuery} terms={matchTerms} />
              </strong>
              <span className="snippet">
                <HighlightedText text={conversation.snippet} query={searchQuery} terms={matchTerms} />
              </span>
              {attachmentNames.length > 0 ? (
                <span className={`attachment-preview ${attachmentMatches.length > 0 || conversation.attachment_content_matched ? "matched" : ""}`}>
                  <Icon name="attach_file" />
                  <HighlightedText
                    text={attachmentMatches.length > 0 ? attachmentMatches.join(", ") : attachmentNames.join(", ")}
                    query={searchQuery}
                    terms={matchTerms}
                  />
                  {conversation.attachment_content_matched ? <span>content matched</span> : null}
                </span>
              ) : conversation.has_attachments ? <Icon name="attach_file" /> : null}
            </span>
            <span className="date">{displayTime(msg.date, datePrefs)}</span>
          </div>
        );
      })}
    </div>
  );
}
