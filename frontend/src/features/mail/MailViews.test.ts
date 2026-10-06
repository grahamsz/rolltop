// @vitest-environment jsdom
import { act, createElement, useState, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { AddToast, ToastUndo } from "../../appTypes";
import type { Conversation, Mailbox } from "../../types";
import { removeMovedMessages } from "../../lib/mailboxMoves";
import { defaultSwipePreferences } from "../../lib/swipeActions";
import { MessageList } from "./MailViews";

let container: HTMLDivElement;
let root: Root;
let undos: ToastUndo[];
let addToast: ReturnType<typeof vi.fn<AddToast>>;

function mailbox(id: number, accountID: number): Mailbox {
  return { id, account_id: accountID, name: `Trash ${accountID}`, role: "trash" } as Mailbox;
}

function conversation(id: number, accountID = 1, messageIDs = [id]): Conversation {
  return {
    message: {
      id, account_id: accountID, mailbox_id: accountID * 10, subject: `Subject ${id}`,
      date: "2026-10-01T12:00:00Z", from_addr: "sender@example.test", to_addr: "reader@example.test",
      snippet: "Preview", is_read: false
    },
    message_ids: messageIDs, message_account_ids: [accountID], count: messageIDs.length,
    participants: "Sender", recipient_participants: "Reader", is_read: false
  } as Conversation;
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  undos = [];
  addToast = vi.fn<AddToast>((_message, _kind, undo) => {
    if (undo) undos.push(undo);
    return addToast.mock.calls.length;
  });
  vi.spyOn(api, "moveMessage").mockResolvedValue({ ok: true, mailbox: "Trash" });
  vi.spyOn(api, "bulkMoveMessages").mockResolvedValue({ ok: true, queued: true, mailbox: "Trash" });
  vi.spyOn(api, "unsnoozeMessage").mockResolvedValue({ ok: true, snoozed: false });
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function render(items: Conversation[], extra: Partial<ComponentProps<typeof MessageList>> = {}) {
  function Harness() {
    const [conversations, setConversations] = useState(items);
    return createElement(MessageList, {
      csrf: "csrf", conversations, hiddenMessageIDs: new Set<number>(),
      mailboxes: [mailbox(11, 1), mailbox(21, 2)], swipePreferences: defaultSwipePreferences(),
      datePrefs: { date_locale: "en-US", date_format: "relative" },
      navigate: vi.fn(), addToast, onStarredChange: vi.fn(), onReadStatesChange: vi.fn(),
      onMessagesMoved: (ids) => setConversations((current) => removeMovedMessages(current, ids)),
      ...extra
    });
  }
  await act(async () => root.render(createElement(Harness)));
}

function checkboxes() {
  return [...container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
}

async function selectAll() {
  for (const checkbox of checkboxes()) await act(async () => checkbox.click());
}

async function clickDelete() {
  const button = container.querySelector<HTMLButtonElement>('button[title="Move selected messages to Trash"]');
  expect(button).not.toBeNull();
  await act(async () => button!.click());
}

it("offers toolbar Delete, restores selection on Undo, and uses each account's Trash", async () => {
  await render([conversation(1), conversation(2, 2)]);
  expect(container.querySelector('[role="toolbar"]')).toBeNull();
  await selectAll();
  await clickDelete();
  expect(checkboxes()).toHaveLength(0);
  expect(api.moveMessage).not.toHaveBeenCalled();
  expect(api.bulkMoveMessages).not.toHaveBeenCalled();
  await act(async () => undos[0].onUndo());
  expect(checkboxes().map((input) => input.checked)).toEqual([true, true]);
  await act(async () => undos[0].onCommit("timeout"));
  expect(api.moveMessage).not.toHaveBeenCalled();
  await clickDelete();
  await act(async () => undos[1].onCommit("timeout"));
  expect(api.moveMessage).toHaveBeenCalledWith("csrf", 1, 11, undefined);
  expect(api.moveMessage).toHaveBeenCalledWith("csrf", 2, 21, undefined);
  expect(checkboxes()).toHaveLength(0);
});

it("refuses the whole selection if an account has no Trash folder", async () => {
  await render([conversation(1), conversation(2, 3)]);
  await selectAll();
  await clickDelete();
  expect(undos).toHaveLength(0);
  expect(checkboxes()).toHaveLength(2);
  expect(addToast).toHaveBeenCalledWith(expect.stringContaining("Choose a Trash folder"), "error");
  expect(api.moveMessage).not.toHaveBeenCalled();
});

it("refuses a thread that spans accounts", async () => {
  await render([{ ...conversation(1, 1, [1, 2]), message_account_ids: [1, 2] }]);
  await selectAll();
  await clickDelete();
  expect(undos).toHaveLength(0);
  expect(addToast).toHaveBeenCalledWith(expect.stringContaining("multiple accounts"), "error");
});

it("does nothing to messages already in the current Trash folder", async () => {
  await render([conversation(1)], { currentMailboxID: 11 });
  await selectAll();
  await clickDelete();
  expect(undos).toHaveLength(0);
  expect(addToast).toHaveBeenCalledWith("Message is already in Trash 1.");
  expect(checkboxes()).toHaveLength(1);
});

it("skips a single-message Trash result while moving other selected search results", async () => {
  const inTrash = conversation(1);
  inTrash.message.mailbox_id = 11;
  await render([inTrash, conversation(2)]);
  await selectAll();
  await clickDelete();
  await act(async () => undos[0].onCommit("timeout"));
  expect(api.moveMessage).toHaveBeenCalledTimes(1);
  expect(api.moveMessage).toHaveBeenCalledWith("csrf", 2, 11, undefined);
  expect(checkboxes()).toHaveLength(1);
  expect(addToast).toHaveBeenCalledWith("Skipped 1 message already in Trash 1.");
});

it("restores and reselects failed rows without restoring successful moves", async () => {
  vi.mocked(api.moveMessage).mockResolvedValueOnce({ ok: true, mailbox: "Trash" }).mockRejectedValueOnce(new Error("unavailable"));
  await render([conversation(1), conversation(2)]);
  await selectAll();
  await clickDelete();
  await act(async () => undos[0].onCommit("timeout"));
  expect(checkboxes()).toHaveLength(1);
  expect(checkboxes()[0].getAttribute("aria-label")).toBe("Select Subject 2");
  expect(checkboxes()[0].checked).toBe(true);
  expect(addToast).toHaveBeenCalledWith("Delete failed: unavailable", "error");
});

it("keeps partially moved threads visible and retries only remaining messages", async () => {
  vi.mocked(api.moveMessage).mockResolvedValueOnce({ ok: true, mailbox: "Trash" }).mockRejectedValueOnce(new Error("unavailable"));
  const thread = conversation(1, 1, [1, 2]);
  thread.message.mailbox_id = 11;
  await render([thread]);
  await selectAll();
  await clickDelete();
  await act(async () => undos[0].onCommit("timeout"));
  expect(checkboxes()).toHaveLength(1);
  expect(checkboxes()[0].checked).toBe(true);
  expect(checkboxes()[0].disabled).toBe(false);
  await clickDelete();
  expect(checkboxes()).toHaveLength(0);
  await act(async () => undos[1].onCommit("timeout"));
  expect(vi.mocked(api.moveMessage).mock.calls.map((call) => call[1])).toEqual([1, 2, 2]);
  expect(checkboxes()).toHaveLength(0);
});

it("clears snooze reminders only after successful moves, including background commits", async () => {
  let finishMove!: () => void;
  vi.mocked(api.moveMessage)
    .mockImplementationOnce(() => new Promise((resolve) => { finishMove = () => resolve({ ok: true, mailbox: "Trash" }); }))
    .mockRejectedValueOnce(new Error("unavailable"));
  await render([conversation(1), conversation(2)], { snoozedView: true });
  await selectAll();
  await clickDelete();
  await act(async () => undos[0].onCommit("background"));
  expect(api.unsnoozeMessage).not.toHaveBeenCalled();
  expect(vi.mocked(api.moveMessage).mock.calls.every((call) => call[3]?.keepalive)).toBe(true);
  await act(async () => finishMove());
  expect(api.unsnoozeMessage).toHaveBeenCalledTimes(1);
  expect(api.unsnoozeMessage).toHaveBeenCalledWith("csrf", 1, { keepalive: true });
  expect(checkboxes()).toHaveLength(1);
});

it("allows independent undo windows and blocks new moves while a commit is running", async () => {
  let finishMove!: () => void;
  vi.mocked(api.moveMessage).mockImplementationOnce(() => new Promise((resolve) => {
    finishMove = () => resolve({ ok: true, mailbox: "Trash" });
  }));
  await render([conversation(1), conversation(2), conversation(3)]);
  await act(async () => checkboxes()[0].click());
  await clickDelete();
  await act(async () => checkboxes()[0].click());
  await clickDelete();
  expect(undos).toHaveLength(2);
  await act(async () => undos[1].onUndo());
  await act(async () => undos[0].onCommit("timeout"));
  const toolbar = container.querySelector('[role="toolbar"]');
  expect(toolbar?.getAttribute("aria-busy")).toBe("true");
  expect(toolbar?.querySelector<HTMLButtonElement>('.selection-delete')?.disabled).toBe(true);
  const drag = new Event("dragstart", { bubbles: true, cancelable: true });
  await act(async () => { container.querySelector('[data-rolltop-list-index]')!.dispatchEvent(drag); });
  expect(drag.defaultPrevented).toBe(true);
  await act(async () => finishMove());
  expect(checkboxes()).toHaveLength(2);
  expect(container.querySelector('[role="toolbar"]')?.getAttribute("aria-busy")).toBe("false");
  expect(api.moveMessage).toHaveBeenCalledTimes(1);
});

it("restores a partially moved swipe thread even when its exit animation is pending", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("RolltopAndroid", { postMessage: vi.fn(), addEventListener: vi.fn() });
  vi.mocked(api.moveMessage).mockResolvedValueOnce({ ok: true, mailbox: "Trash" }).mockRejectedValueOnce(new Error("unavailable"));
  await render([conversation(1, 1, [1, 2])], {
    swipePreferences: { ...defaultSwipePreferences(), right_action: "trash" }
  });
  const row = container.querySelector('[data-rolltop-list-index]')!;
  async function touch(type: string, clientX: number) {
    const event = new Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, "touches", { value: [{ clientX, clientY: 10 }] });
    await act(async () => { row.dispatchEvent(event); });
  }
  await touch("touchstart", 10);
  await touch("touchmove", 110);
  await touch("touchend", 110);
  await act(async () => { await vi.advanceTimersByTimeAsync(170); });
  expect(undos).toHaveLength(1);
  await act(async () => undos[0].onCommit("dismiss"));
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(checkboxes()).toHaveLength(1);
  await selectAll();
  await clickDelete();
  await act(async () => undos[1].onCommit("timeout"));
  expect(vi.mocked(api.moveMessage).mock.calls.map((call) => call[1])).toEqual([1, 2, 2]);
  expect(checkboxes()).toHaveLength(0);
});
