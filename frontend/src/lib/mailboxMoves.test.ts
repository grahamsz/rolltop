import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { executeMailboxMove } from "./mailboxMoves";

afterEach(() => vi.restoreAllMocks());

it("tracks successful chunks when a different chunk fails", async () => {
  const ids = Array.from({ length: 2001 }, (_, index) => index + 1);
  const failure = new Error("second chunk failed");
  const bulk = vi.spyOn(api, "bulkMoveMessages")
    .mockResolvedValueOnce({ ok: true, queued: true, mailbox: "Trash" })
    .mockRejectedValueOnce(failure);
  const single = vi.spyOn(api, "moveMessage").mockResolvedValue({ ok: true, mailbox: "Trash" });
  const result = await executeMailboxMove("csrf", 40, ids);
  expect(result).toEqual({ movedIDs: [...ids.slice(0, 1000), 2001], queuedCount: 1000, error: failure });
  expect(bulk.mock.calls.map((call) => call[1].length)).toEqual([1000, 1000]);
  expect(single).toHaveBeenCalledWith("csrf", 2001, 40, undefined);
});

it("dispatches every background chunk before awaiting and reports quota failures", async () => {
  const ids = Array.from({ length: 7000 }, (_, index) => index + 1);
  const failure = new TypeError("keepalive quota exceeded");
  const bulk = vi.spyOn(api, "bulkMoveMessages").mockImplementation(async (_csrf, chunk) => {
    if (chunk[0] > 5000) throw failure;
    return { ok: true, queued: true, mailbox: "Trash" };
  });
  const pending = executeMailboxMove("csrf", 40, ids, true);
  expect(bulk).toHaveBeenCalledTimes(7);
  expect(bulk.mock.calls.flatMap((call) => call[1])).toEqual(ids);
  expect(bulk.mock.calls.every((call) => call[3]?.keepalive)).toBe(true);
  expect(await pending).toEqual({ movedIDs: ids.slice(0, 5000), queuedCount: 5000, error: failure });
});

it("tracks partial small moves even when committing in the background", async () => {
  const failure = new Error("second message failed");
  const single = vi.spyOn(api, "moveMessage")
    .mockResolvedValueOnce({ ok: true, mailbox: "Trash" })
    .mockRejectedValueOnce(failure);
  const bulk = vi.spyOn(api, "bulkMoveMessages");
  const pending = executeMailboxMove("csrf", 40, [1, 2], true);
  expect(single).toHaveBeenCalledTimes(2);
  expect(single.mock.calls.every((call) => call[3]?.keepalive)).toBe(true);
  expect(await pending).toEqual({ movedIDs: [1], queuedCount: 0, error: failure });
  expect(bulk).not.toHaveBeenCalled();
});
