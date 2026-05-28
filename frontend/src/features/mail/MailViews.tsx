// File overview: Mailbox and search result lists. These components fetch paged conversations,
// surface sync clues, keep selection state stable, and link rows back to their source page.

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ChangeEvent, DragEvent, KeyboardEvent, MouseEvent, ReactNode } from "react";
import { Star } from "@phosphor-icons/react";
import { api } from "../../api";
import type { DatePrefs, Toast, LocationState } from "../../appTypes";
import type { Bootstrap, Conversation, Mailbox, SyncRun } from "../../types";
import { Icon } from "../../components/Icon";
import { ListHeader } from "../../components/common";
import { messageFromError } from "../../lib/errors";
import { displayTime } from "../../lib/format";
import { effectiveMailboxSyncMode, mailboxActiveRun, mailboxNeedsSync, mailboxRefreshKey } from "../../lib/sync";
import { pgpPreviewText } from "../../lib/pgpPreview";
import { HighlightedText } from "../../lib/searchHighlight";
import { mailPageSize } from "../../lib/constants";
import { mailRoute, mailURL, messageURL, routeWithSearch, searchRoute, searchURL } from "../../lib/routes";

/**
 * MailView fetches one page of mailbox/all-mail conversations. It clears stale
 * rows when the URL changes, animates newly delivered messages on the first page,
 * and shows a folder-level sync clue when the selected mailbox is manual or off.
 */
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
  const listScopeKey = mailboxID || "all";
  const listKey = listScopeKey + ":" + page;
  const slideDirection = useListSlideDirection(listScopeKey, page);
  const cachedTransitionPage = previousListKey.current !== listKey ? api.cachedMail(mailboxID, page) : null;
  const displayConversations = cachedTransitionPage?.conversations || conversations;
  const displayHasPrev = cachedTransitionPage?.has_prev ?? hasPrev;
  const displayHasNext = cachedTransitionPage?.has_next ?? hasNext;
  const listPending = (loading || previousListKey.current !== listKey) && !cachedTransitionPage;
  const listTransitionSpeed: SlideSpeed = cachedTransitionPage ? "fast" : listPending ? "slow" : "fast";
  const activeRun = mailboxActiveRun(mailbox, activeSyncRuns, latestSyncRun);
  const effectiveMode = mailbox ? effectiveMailboxSyncMode(mailbox, mailboxes) : "auto";

  useEffect(() => {
    return () => {
      if (newMessageTimer.current !== null) window.clearTimeout(newMessageTimer.current);
    };
  }, []);

  // Route changes should feel immediate: clear the old page before the server
  // responds so the user is not looking at stale rows for another folder.
  useEffect(() => {
    let cancelled = false;
    const isNewList = previousListKey.current !== listKey;
    const canAnimateNewMail = page === 1 && loaded.current && !isNewList && Boolean(refreshKey) && Boolean(latestSyncRun?.new_messages);
    if (isNewList || !loaded.current) {
      const cached = api.cachedMail(mailboxID, page);
      if (cached) {
        previousPageIDs.current = new Set(cached.conversations.map((conversation) => conversation.message.id));
        previousListKey.current = listKey;
        setConversations(cached.conversations);
        setHasPrev(cached.has_prev);
        setHasNext(cached.has_next);
        setLoading(false);
      } else {
        setLoading(true);
        setConversations([]);
        setHasPrev(false);
        setHasNext(false);
      }
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
            newMessageTimer.current = window.setTimeout(() => setNewMessageIDs(new Set()), 1200);
          }
        } else {
          setNewMessageIDs(new Set());
        }
        previousPageIDs.current = nextIDs;
        previousListKey.current = listKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
        if (data.has_next) api.prefetchMail(mailboxID, page + 1);
        if (data.has_prev && page > 1) api.prefetchMail(mailboxID, page - 1);
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
    if (effectiveMode === "never") {
      addToast(`${mailbox.name} is set to Never. Change the folder sync mode before syncing.`, "error");
      return;
    }
    setSyncBusy(true);
    try {
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
          itemCount: listPending ? 0 : displayConversations.length,
          total: totalCount,
          hasPrev: listPending ? false : displayHasPrev,
          hasNext: listPending ? false : displayHasNext,
          pageURL,
          navigate,
          ariaLabel: "Mailbox pagination",
          loading: listPending
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
      {!error ? (
        <SlidingMessageListStage stageKey={listKey} direction={slideDirection} pending={listPending} speed={listTransitionSpeed}>
          {listPending ? (
            <MessageListSkeleton label="Loading messages" />
          ) : (
            <MessageList
              csrf={csrf}
              conversations={displayConversations}
              hiddenMessageIDs={hiddenMessageIDs}
              highlightMessageIDs={newMessageIDs}
              showRecipients={mailbox?.role === "sent" || mailbox?.role === "drafts"}
              openAsDraft={mailbox?.role === "drafts"}
              datePrefs={datePrefs}
              returnURL={mailURL(mailboxID, page)}
              navigate={navigate}
              addToast={addToast}
              onStarredChange={updateStarred}
            />
          )}
        </SlidingMessageListStage>
      ) : null}
    </>
  );
}

// FolderSyncNotice is shown only when the selected folder is known to be
// excluded from automatic sync or behind the remote mailbox.
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
    ? "This folder is excluded from sync. Change its sync mode in folder settings before mirroring it."
    : "This manual-sync folder is behind the remote mailbox. Sync it to mirror the latest messages.";
  const buttonLabel = busy ? "Starting" : "Sync folder";

  return (
    <section className="folder-sync-notice" aria-live="polite">
      <Icon name="report" />
      <div className="folder-sync-copy">
        <strong>{title}</strong>
        <span>{detail}</span>
      </div>
      {!syncOff ? (
        <button className="secondary" type="button" disabled={busy} onClick={onSync}>
          <Icon name="sync" />
          {buttonLabel}
        </button>
      ) : null}
    </section>
  );
}


function activeSearchMaintenanceRun(runs: SyncRun[]): SyncRun | null {
  return runs.find((run) => {
    const subject = (run.latest_new_subject || "").toLowerCase();
    return subject === "purging full-text index" ||
      subject === "purging local references and full-text index" ||
      subject === "repairing full-text index" ||
      subject.includes("search index repair");
  }) || null;
}

function SearchMaintenanceNotice({ run }: { run: SyncRun }) {
  const total = Math.max(0, run.messages_total || 0);
  const seen = Math.max(0, run.messages_seen || 0);
  const done = total > 0 ? Math.min(seen, total) : seen;
  const remaining = total > 0 ? Math.max(0, total - done) : 0;
  const label = run.latest_new_subject || "Full-text indexing";
  const scope = run.current_mailbox ? ` in ${run.current_mailbox}` : "";
  const progress = total > 0
    ? `${done.toLocaleString()} of ${total.toLocaleString()} messages checked`
    : done > 0 ? `${done.toLocaleString()} messages checked` : "Index work is running";
  const remainingText = remaining > 0 ? `, ${remaining.toLocaleString()} remaining` : "";

  return (
    <section className="folder-sync-notice search-maintenance-notice running" aria-live="polite">
      <Icon name="report" />
      <div className="folder-sync-copy">
        <strong>Search may be slow</strong>
        <span>{label}{scope}. {progress}{remainingText}.</span>
      </div>
    </section>
  );
}

/**
 * SearchView is always best-match search. The URL carries the query and page so
 * opening a result can preserve a precise back target to the same result page.
 */
export function SearchView({
  csrf,
  location,
  navigate,
  hiddenMessageIDs,
  datePrefs,
  activeSyncRuns,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  datePrefs: DatePrefs;
  activeSyncRuns: SyncRun[];
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
  const searchKey = query + ":best:" + page;
  const slideDirection = useListSlideDirection("search:" + query, page);
  const listPending = loading || loadedKey.current !== searchKey;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setConversations([]);
    setHasPrev(false);
    setHasNext(false);
    setError("");
    api
      .search(query, page)
      .then((data) => {
        if (cancelled) return;
        loadedKey.current = searchKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
        if (data.has_next) api.prefetchSearch(query, page + 1);
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
  const maintenanceRun = activeSearchMaintenanceRun(activeSyncRuns);

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
          ariaLabel: "Search pagination",
          loading: listPending
        }}
      />
      {query ? <div className="muted">Results for <strong>{query}</strong></div> : null}
      {maintenanceRun ? <SearchMaintenanceNotice run={maintenanceRun} /> : null}
      {error ? <div className="error">{error}</div> : null}
      {!error ? (
        <SlidingMessageListStage stageKey={searchKey} direction={slideDirection} pending={listPending} speed={listPending ? "slow" : "fast"}>
          {listPending ? (
            <MessageListSkeleton label="Searching" />
          ) : (
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
          )}
        </SlidingMessageListStage>
      ) : null}
    </>
  );
}

type SlideDirection = "left" | "right" | "none";
type SlideSpeed = "fast" | "slow";

type OutgoingListPane = {
  key: string;
  child: ReactNode;
  direction: Exclude<SlideDirection, "none">;
};

function useListSlideDirection(scopeKey: string, page: number): SlideDirection {
  const previous = useRef({ scopeKey, page });
  const direction = useRef<SlideDirection>("none");
  if (previous.current.scopeKey !== scopeKey || previous.current.page !== page) {
    direction.current = previous.current.scopeKey === scopeKey && page !== previous.current.page
      ? page > previous.current.page ? "left" : "right"
      : "none";
    previous.current = { scopeKey, page };
  }
  return direction.current;
}

function SlidingMessageListStage({
  stageKey,
  direction,
  pending,
  speed,
  children
}: {
  stageKey: string;
  direction: SlideDirection;
  pending: boolean;
  speed: SlideSpeed;
  children: ReactNode;
}) {
  const lastPane = useRef({ key: stageKey, child: children });
  const measuredHeight = useRef(0);
  const currentPane = useRef<HTMLDivElement | null>(null);
  const [outgoing, setOutgoing] = useState<OutgoingListPane | null>(null);
  const [lockedHeight, setLockedHeight] = useState<number | null>(null);

  useLayoutEffect(() => {
    if (lastPane.current.key === stageKey) return;
    const previous = lastPane.current;
    if (measuredHeight.current > 0) setLockedHeight(measuredHeight.current);
    if (direction !== "none") {
      setOutgoing({ key: previous.key, child: previous.child, direction });
      const timer = window.setTimeout(() => {
        setOutgoing((current) => current?.key === previous.key ? null : current);
      }, speed === "slow" ? 640 : 140);
      lastPane.current = { key: stageKey, child: children };
      return () => window.clearTimeout(timer);
    }
    lastPane.current = { key: stageKey, child: children };
    setOutgoing(null);
  }, [stageKey, direction, speed, children]);

  useLayoutEffect(() => {
    lastPane.current = { key: stageKey, child: children };
    if (!pending && currentPane.current) {
      measuredHeight.current = currentPane.current.offsetHeight;
    }
  });

  useLayoutEffect(() => {
    if (!pending && !outgoing) setLockedHeight(null);
  }, [pending, outgoing]);

  const stageStyle = lockedHeight ? { minHeight: `${lockedHeight}px` } : undefined;
  if (outgoing) {
    const incomingPane = (
      <div className="message-list-pane incoming" key={stageKey} ref={currentPane}>
        {children}
      </div>
    );
    const outgoingPane = (
      <div className="message-list-pane outgoing" key={`out-${outgoing.key}`}>
        {outgoing.child}
      </div>
    );
    return (
      <div className={`message-list-stage speed-${speed}`} style={stageStyle}>
        <div
          className={`message-list-track slide-${outgoing.direction}`}
          onAnimationEnd={() => setOutgoing((current) => current?.key === outgoing.key ? null : current)}
        >
          {outgoing.direction === "right" ? incomingPane : outgoingPane}
          {outgoing.direction === "right" ? outgoingPane : incomingPane}
        </div>
      </div>
    );
  }
  return (
    <div className={`message-list-stage speed-${speed}`} style={stageStyle}>
      <div className="message-list-pane" key={stageKey} ref={currentPane}>
        {children}
      </div>
    </div>
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

function messageDragPreview(conversations: Conversation[], ids: number[]) {
  if (typeof document === "undefined" || ids.length === 0) return null;
  const idSet = new Set(ids);
  const rows = conversations.filter((conversation) => idSet.has(conversation.message.id));
  const preview = document.createElement("div");
  preview.className = "message-drag-preview";
  preview.setAttribute("aria-hidden", "true");
  const count = ids.length;
  const title = document.createElement("div");
  title.className = "message-drag-preview-count";
  title.textContent = count === 1 ? "1 message" : `${count.toLocaleString()} messages`;
  preview.appendChild(title);
  rows.slice(0, 4).forEach((conversation) => {
    const line = document.createElement("div");
    line.className = "message-drag-preview-row";
    const sender = conversation.participants || conversation.message.from_addr || "Unknown sender";
    const subject = conversation.message.subject || "(no subject)";
    line.textContent = `${sender} - ${subject}`;
    preview.appendChild(line);
  });
  if (count > rows.length || count > 4) {
    const more = document.createElement("div");
    more.className = "message-drag-preview-more";
    more.textContent = `+${Math.max(0, count - Math.min(rows.length, 4)).toLocaleString()} more`;
    preview.appendChild(more);
  }
  document.body.appendChild(preview);
  return preview;
}

// MessageList is shared by mailbox and search pages. It owns local row selection,
// shift-select ranges, drag payloads, optimistic star updates, and message links.
function MessageList({
  csrf,
  conversations,
  hiddenMessageIDs,
  highlightMessageIDs,
  showRecipients = false,
  openAsDraft = false,
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
  showRecipients?: boolean;
  openAsDraft?: boolean;
  searchQuery?: string;
  datePrefs: DatePrefs;
  returnURL?: string;
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onStarredChange: (messageID: number, starredMessageID: number, starred: boolean) => void;
}) {
  const [selectedIDs, setSelectedIDs] = useState<Set<number>>(() => new Set());
  const [dismissedIDs, setDismissedIDs] = useState<Set<number>>(() => new Set());
  const lastSelectedIndex = useRef<number | null>(null);
  const moveOutTimers = useRef<Map<number, number>>(new Map());
  const visible = conversations.filter((conversation) => !dismissedIDs.has(conversation.message.id));
  const visibleKey = visible.map((conversation) => conversation.message.id).join(",");
  const sourceKey = conversations.map((conversation) => conversation.message.id).join(",");
  const hiddenKey = Array.from(hiddenMessageIDs).sort((a, b) => a - b).join(",");

  useEffect(() => {
    return () => {
      moveOutTimers.current.forEach((timer) => window.clearTimeout(timer));
      moveOutTimers.current.clear();
    };
  }, []);

  useEffect(() => {
    const sourceIDs = new Set(conversations.map((conversation) => conversation.message.id));
    setDismissedIDs((current) => {
      const next = new Set<number>();
      current.forEach((id) => {
        if (sourceIDs.has(id) && hiddenMessageIDs.has(id)) next.add(id);
      });
      return next.size === current.size ? current : next;
    });
    sourceIDs.forEach((id) => {
      if (hiddenMessageIDs.has(id)) {
        if (!moveOutTimers.current.has(id)) {
          const timer = window.setTimeout(() => {
            moveOutTimers.current.delete(id);
            setDismissedIDs((current) => {
              const next = new Set(current);
              next.add(id);
              return next;
            });
          }, 230);
          moveOutTimers.current.set(id, timer);
        }
      } else {
        const timer = moveOutTimers.current.get(id);
        if (timer !== undefined) {
          window.clearTimeout(timer);
          moveOutTimers.current.delete(id);
        }
      }
    });
  }, [conversations, hiddenKey, sourceKey, hiddenMessageIDs]);

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

  function startMessageDrag(event: DragEvent<HTMLDivElement>, conversation: Conversation) {
    const ids = selectedDragIDs(conversation.message.id);
    const selected = visible.filter((item) => ids.includes(item.message.id));
    const accountIDs = Array.from(new Set(selected.map((item) => item.message.account_id).filter((id) => Number.isFinite(id) && id > 0)));
    event.dataTransfer.effectAllowed = "copyMove";
    event.dataTransfer.setData("application/x-mailmirror-message-transfer", JSON.stringify({ ids, account_ids: accountIDs }));
    event.dataTransfer.setData("application/x-mailmirror-messages", JSON.stringify(ids));
    event.dataTransfer.setData("application/x-mailmirror-message", String(ids[0]));
    event.dataTransfer.setData("text/plain", String(ids[0]));
    const dragImage = messageDragPreview(visible, ids);
    if (dragImage) {
      event.dataTransfer.setDragImage(dragImage, 18, 18);
      window.setTimeout(() => dragImage.remove(), 0);
    }
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
  const arrivalActive = visible.some((conversation) => highlightMessageIDs?.has(conversation.message.id));
  return (
    <div className={`message-table ${arrivalActive ? "mail-arrival-shift" : ""}`}>
      {visible.map((conversation, index) => {
        const msg = conversation.message;
        const matchTerms = conversation.match_terms || [];
        const href = openAsDraft ? `/compose?draft=${msg.id}` : messageURL(msg.id, searchQuery, matchTerms, returnURL, searchQuery ? msg.id : 0);
        const attachmentNames = conversation.attachment_names || [];
        const attachmentMatches = conversation.attachment_matches || [];
        const previewText = pgpPreviewText(conversation.snippet, msg.is_encrypted, msg.is_signed);
        const selected = selectedIDs.has(msg.id);
        const movingOut = hiddenMessageIDs.has(msg.id);
        const participantText = showRecipients
          ? `To: ${conversation.recipient_participants || msg.to_addr || conversation.participants || "undisclosed recipients"}`
          : (conversation.participants || msg.from_addr || "Unknown sender");
        return (
          <div
            className={`message-row ${conversation.is_read ? "read" : "unread"} ${selected ? "selected" : ""} ${movingOut ? "moving-out" : ""} ${highlightMessageIDs?.has(msg.id) ? "new-delivery" : ""}`}
            draggable
            key={msg.id}
            role="link"
            tabIndex={0}
            onClick={(event) => openRow(event, href)}
            onKeyDown={(event) => openRowWithKeyboard(event, href)}
            onDragStart={(event) => startMessageDrag(event, conversation)}
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
                <HighlightedText text={participantText} query={searchQuery} terms={matchTerms} />
              </span>
              {conversation.count > 1 ? <span className="thread-count">({conversation.count})</span> : null}
            </span>
            <span className="subject">
              <strong>
                <HighlightedText text={msg.subject || "(no subject)"} query={searchQuery} terms={matchTerms} />
              </strong>
              {msg.is_encrypted || msg.is_signed ? (
                <span className="message-pgp-icons" aria-label={[msg.is_encrypted ? "Encrypted" : "", msg.is_signed ? "Signed" : ""].filter(Boolean).join(", ")}>
                  {msg.is_encrypted ? <span className="message-pgp-icon encrypted" title="Encrypted message"><Icon name="lock" weight="bold" /></span> : null}
                  {msg.is_signed ? <span className="message-pgp-icon signature pending" title="Signature pending verification"><Icon name="signature" weight="bold" /></span> : null}
                </span>
              ) : null}
              <span className={`snippet ${msg.is_encrypted ? "encrypted-preview" : ""}`}>
                <HighlightedText text={previewText} query={msg.is_encrypted ? "" : searchQuery} terms={msg.is_encrypted ? [] : matchTerms} />
              </span>
              {attachmentNames.length > 0 ? (
                <span className={`attachment-preview ${attachmentMatches.length > 0 || conversation.attachment_content_matched ? "matched" : ""}`}>
                  <Icon name="attach_file" />
                  <HighlightedText
                    text={attachmentMatches.length > 0 ? attachmentMatches.join(", ") : attachmentNames.join(", ")}
                    query={searchQuery}
                    terms={matchTerms}
                  />
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
