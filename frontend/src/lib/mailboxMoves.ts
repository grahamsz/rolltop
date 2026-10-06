import { api } from "../api";
import type { Conversation } from "../types";

// Bulk endpoints accept at most 1000 IDs and queue batches larger than five.
// Smaller batches use individual requests because the inline bulk endpoint
// cannot report which messages succeeded when a later move fails.
const bulkMessageIDLimit = 1000;
const inlineMoveMessageLimit = 5;

export async function executeMailboxMove(csrf: string, mailboxID: number, messageIDs: number[], keepalive = false) {
  const options = keepalive ? { keepalive: true } : undefined;
  const operations: { ids: number[]; result: Promise<{ queued?: boolean }> }[] = [];
  for (let start = 0; start < messageIDs.length; start += bulkMessageIDLimit) {
    const ids = messageIDs.slice(start, start + bulkMessageIDLimit);
    if (ids.length > inlineMoveMessageLimit) {
      operations.push({ ids, result: api.bulkMoveMessages(csrf, ids, mailboxID, options) });
    } else {
      for (const id of ids) {
        operations.push({ ids: [id], result: api.moveMessage(csrf, id, mailboxID, options).then(() => ({ queued: false })) });
      }
    }
  }
  // Hand every request to the browser before awaiting, including on unload.
  // The browser enforces its shared keepalive quota; rejected requests must be
  // reported as failures, never silently omitted or counted as successful.
  const results = await Promise.allSettled(operations.map((operation) => operation.result));
  const movedIDs: number[] = [];
  let queuedCount = 0;
  let error: unknown;
  results.forEach((result, index) => {
    if (result.status === "fulfilled") {
      movedIDs.push(...operations[index].ids);
      if (result.value.queued) queuedCount += operations[index].ids.length;
    } else if (error === undefined) {
      error = result.reason;
    }
  });
  return { movedIDs, queuedCount, error };
}

/** Keep failed messages visible and retry only those that have not moved. */
export function removeMovedMessages(conversations: Conversation[], movedIDs: number[]): Conversation[] {
  const moved = new Set(movedIDs);
  return conversations.flatMap((conversation) => {
    const ids = conversation.message_ids?.length ? conversation.message_ids : [conversation.message.id];
    const remaining = ids.filter((id) => !moved.has(id));
    if (remaining.length === 0) return [];
    if (remaining.length === ids.length) return [conversation];
    // Retain the representative so the full thread can still be opened until
    // the next server refresh supplies an updated summary.
    return [{ ...conversation, message_ids: remaining, count: remaining.length }];
  });
}
