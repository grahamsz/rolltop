// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { SyncRun, SyncRunLiveDetail } from "../../types";
import { SyncRunView } from "./SettingsViews";

let container: HTMLDivElement;
let root: Root;

const inactive: SyncRunLiveDetail = { active: false, cancellable: false, phase: "", detail: "", phase_started_at: "" };

function run(status: string): SyncRun {
  return {
    id: 42, account_id: 1, status, current_mailbox: "INBOX", current_uid: 0,
    started_at: "2026-10-06T19:44:00Z", updated_at: "2026-10-06T19:44:01Z", finished_at: "",
    messages_seen: 0, messages_stored: 0, messages_skipped: 0, messages_total: 0,
    mailboxes_done: 0, mailboxes_total: 0, new_messages: 0, latest_new_from: "",
    latest_new_subject: "", latest_new_message_id: 0, error: ""
  };
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.useFakeTimers();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function render() {
  await act(async () => root.render(createElement(SyncRunView, {
    csrf: "csrf", location: { path: "/sync-runs/42", search: "" },
    navigate: vi.fn(), datePrefs: { date_locale: "en-US", date_format: "relative" }
  })));
}

it.each([
  ["ok", "Sync complete"], ["failed", "Sync failed"], ["interrupted", "Sync interrupted"]
])("shows the outcome of a %s run when live activity disappears", async (status, title) => {
  vi.spyOn(api, "syncRun").mockResolvedValue({ sync_run: run(status), live: inactive });
  await render();
  expect(container.querySelector("h2")?.textContent).toBe(title);
  expect(container.querySelector(".sync-run-progress.indeterminate")).toBeNull();
  expect(container.querySelector(".sync-run-progress-copy")?.textContent).not.toContain("Live");
});

it("switches from live activity to completion on the next status response", async () => {
  const response = vi.spyOn(api, "syncRun").mockResolvedValue({
    sync_run: run("running"), live: { ...inactive, active: true, cancellable: true, phase: "imap-fetch" }
  });
  await render();
  expect(container.querySelector("h2")?.textContent).toBe("Fetching mail");
  expect(container.querySelector(".sync-run-progress.indeterminate")).not.toBeNull();
  response.mockResolvedValue({ sync_run: run("ok"), live: inactive });
  await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
  expect(container.querySelector("h2")?.textContent).toBe("Sync complete");
  expect(container.querySelector(".sync-run-progress.indeterminate")).toBeNull();
  expect(container.querySelector(".sync-run-cancel")).toBeNull();
});

it("does not treat missing live details as a worker starting", async () => {
  vi.spyOn(api, "syncRun").mockResolvedValue({ sync_run: run("running"), live: inactive });
  await render();
  expect(container.querySelector("h2")?.textContent).toBe("Waiting for sync details");
  expect(container.querySelector(".sync-run-progress.indeterminate")).toBeNull();
});
