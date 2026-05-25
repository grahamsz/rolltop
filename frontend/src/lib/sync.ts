// File overview: Sync-state helpers shared by mailbox lists, sidebar status, and settings rows.

import type { Mailbox, SyncRun } from "../types";
import { folderParentNames } from "./folders";

/** mailboxRefreshKey returns a stable key when a sync run should refresh a mailbox list. */
export function mailboxRefreshKey(run: SyncRun | null, mailbox: Mailbox | undefined): string {
  if (!run || run.messages_stored <= 0) return "";
  const current = run.current_mailbox.trim().toLowerCase();
  const active = mailbox?.name.trim().toLowerCase() || "";
  if (mailbox && run.account_id && run.account_id !== mailbox.account_id) return "";
  if (active && current && active !== current) return "";
  return `${run.id}:${run.current_mailbox}:${run.messages_stored}:${run.status}`;
}

/** normalizedSyncMode canonicalizes folder sync modes for UI comparisons. */
export function normalizedSyncMode(mode: string): string {
  const value = mode.trim().toLowerCase();
  if (value === "manual" || value === "never" || value === "inherit") return value;
  return "auto";
}

/** effectiveMailboxSyncMode resolves inherited folder sync mode against account/folder defaults. */
export function effectiveMailboxSyncMode(mailbox: Mailbox, mailboxes: Mailbox[]): string {
  const direct = normalizedSyncMode(mailbox.sync_mode || "");
  if (direct !== "inherit") return direct;
  const byName = new Map(mailboxes.map((item) => [item.name.trim().toLowerCase(), item]));
  for (const parent of folderParentNames(mailbox.name)) {
    const parentMailbox = byName.get(parent.trim().toLowerCase());
    if (!parentMailbox) continue;
    const parentMode = normalizedSyncMode(parentMailbox.sync_mode || "");
    if (parentMode !== "inherit") return parentMode;
  }
  return "auto";
}

/** syncPercent returns the best available local mirror progress percentage for a mailbox. */
export function syncPercent(mailbox: Mailbox): number {
  const value = Number(mailbox.sync_percent || 0);
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, Math.round(value)));
}

/** mailboxNeedsSync reports whether remote counts suggest a manual folder is behind. */
export function mailboxNeedsSync(mailbox: Mailbox): boolean {
  if (mailbox.remote_uid_next > 1 && mailbox.last_uid < mailbox.remote_uid_next - 1) return true;
  return syncPercent(mailbox) > 0 && syncPercent(mailbox) < 100;
}

/** mailboxActiveRun finds the sync run currently touching a mailbox, if any. */
export function mailboxActiveRun(mailbox: Mailbox | undefined, activeRuns: SyncRun[], latestRun: SyncRun | null): SyncRun | null {
  if (!mailbox) return null;
  const name = mailbox.name.trim().toLowerCase();
  const runs = mergeSyncRuns(activeRuns, latestRun ? [latestRun] : []);
  return runs.find((run) => run.status === "running" && (!run.account_id || run.account_id === mailbox.account_id) && run.current_mailbox.trim().toLowerCase() === name) || null;
}

/** mergeSyncRuns merges active and historical sync runs without duplicate IDs. */
export function mergeSyncRuns(primary: SyncRun[], rest: SyncRun[]): SyncRun[] {
  const seen = new Set<number>();
  const out: SyncRun[] = [];
  for (const run of [...primary, ...rest]) {
    if (seen.has(run.id)) continue;
    seen.add(run.id);
    out.push(run);
  }
  return out;
}

/** syncRunProgress calculates progress from message counts, folder counts, or fallback state. */
export function syncRunProgress(run: SyncRun, fallback: number): number {
  if (run.messages_total > 0) {
    return Math.max(0, Math.min(100, Math.round((run.messages_seen / run.messages_total) * 100)));
  }
  return fallback;
}
