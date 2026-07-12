// Bounded, user-scoped persistence for the first All Mail page. The snapshot
// paints only after bootstrap has confirmed the same authenticated user.

import type { Conversation, MailListResponse, Message } from "../types";

const snapshotVersion = 1;
const maxSnapshotBytes = 900_000;
const maxSnapshotConversations = 200;

type AllMailSnapshot = {
  version: number;
  user_id: number;
  saved_at: number;
  page: MailListResponse;
};

export function allMailSnapshotStorageKey(userID: number): string {
  return `rolltop.mail.all.v${snapshotVersion}.${userID}`;
}

export function loadAllMailSnapshot(userID: number): MailListResponse | null {
  if (!positiveInteger(userID)) return null;
  try {
    const parsed = JSON.parse(localStorage.getItem(allMailSnapshotStorageKey(userID)) || "null") as unknown;
    if (!validSnapshot(parsed, userID)) return null;
    return parsed.page;
  } catch {
    return null;
  }
}

export function saveAllMailSnapshot(userID: number, page: MailListResponse): boolean {
  if (!positiveInteger(userID) || !validMailPage(page)) return false;
  const snapshot: AllMailSnapshot = {
    version: snapshotVersion,
    user_id: userID,
    saved_at: Date.now(),
    page
  };
  try {
    const serialized = JSON.stringify(snapshot);
    if (new TextEncoder().encode(serialized).byteLength > maxSnapshotBytes) return false;
    localStorage.setItem(allMailSnapshotStorageKey(userID), serialized);
    return true;
  } catch {
    return false;
  }
}

export function clearAllMailSnapshot(userID: number) {
  if (!positiveInteger(userID)) return;
  try {
    localStorage.removeItem(allMailSnapshotStorageKey(userID));
  } catch {
    // Storage may be unavailable in private or locked-down browser contexts.
  }
}

export function clearOtherAllMailSnapshots(keepUserID: number) {
  const prefix = `rolltop.mail.all.v${snapshotVersion}.`;
  try {
    const keys: string[] = [];
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index) || "";
      if (key.startsWith(prefix) && key !== allMailSnapshotStorageKey(keepUserID)) keys.push(key);
    }
    keys.forEach((key) => localStorage.removeItem(key));
  } catch {
    // Best-effort privacy cleanup for storage-restricted browsers.
  }
}

function validSnapshot(value: unknown, userID: number): value is AllMailSnapshot {
  if (!record(value)) return false;
  return value.version === snapshotVersion && value.user_id === userID &&
    typeof value.saved_at === "number" && Number.isFinite(value.saved_at) && value.saved_at > 0 &&
    validMailPage(value.page);
}

function validMailPage(value: unknown): value is MailListResponse {
  if (!record(value) || value.page !== 1 || value.has_prev !== false || typeof value.has_next !== "boolean") return false;
  if (!Array.isArray(value.conversations) || value.conversations.length > maxSnapshotConversations) return false;
  return value.conversations.every(validConversation);
}

function validConversation(value: unknown): value is Conversation {
  if (!record(value) || !validMessage(value.message)) return false;
  return positiveInteger(value.starred_message_id) && typeof value.participants === "string" &&
    typeof value.recipient_participants === "string" && positiveInteger(value.count) &&
    typeof value.is_read === "boolean" && typeof value.has_attachments === "boolean" &&
    typeof value.snippet === "string" && optionalPositiveIntegerArray(value.message_ids) &&
    optionalPositiveIntegerArray(value.message_account_ids) && optionalStringArray(value.attachment_names) &&
    optionalStringArray(value.attachment_matches) && optionalBoolean(value.attachment_content_matched) &&
    optionalStringArray(value.match_terms) && optionalStringArray(value.match_query_terms) &&
    optionalString(value.snoozed_until);
}

function validMessage(value: unknown): value is Message {
  if (!record(value)) return false;
  return positiveInteger(value.id) && positiveInteger(value.account_id) && positiveInteger(value.mailbox_id) &&
    typeof value.subject === "string" && typeof value.from_addr === "string" &&
    typeof value.to_addr === "string" && typeof value.cc_addr === "string" &&
    typeof value.date === "string" && typeof value.date_short === "string" &&
    typeof value.is_read === "boolean" && typeof value.is_starred === "boolean" &&
    typeof value.has_attachments === "boolean" && typeof value.is_encrypted === "boolean" &&
    typeof value.is_signed === "boolean" && typeof value.snippet === "string";
}

function record(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function positiveInteger(value: unknown): boolean {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}

function optionalPositiveIntegerArray(value: unknown): boolean {
  return value === undefined || (Array.isArray(value) && value.every(positiveInteger));
}

function optionalStringArray(value: unknown): boolean {
  return value === undefined || (Array.isArray(value) && value.every((item) => typeof item === "string"));
}

function optionalBoolean(value: unknown): boolean {
  return value === undefined || typeof value === "boolean";
}

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === "string";
}
