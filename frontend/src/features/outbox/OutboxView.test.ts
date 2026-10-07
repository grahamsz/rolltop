// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { api } from "../../api";
import type { OutboxJob } from "../../types";
import { OutboxView } from "./OutboxView";

it("describes a Sent-copy retry as pending after SMTP acceptance", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const summary = { active: 1, needs_attention: 0, latest_id: 1 };
  const job: OutboxJob = {
    id: 1, message_id: 42, subject: "Test message", delivery_state: "accepted", filing_state: "retry_wait",
    attempt_count: 1, filing_attempt_count: 4, next_attempt_at: "", last_error: "Message delivered; Rolltop will retry saving it to Sent.",
    needs_attention: false, can_cancel: false, can_retry: false, retry_may_duplicate: false,
    smtp_accepted_at: "2026-10-07T21:00:00Z", completed_at: "", created_at: "2026-10-07T21:00:00Z",
    updated_at: "2026-10-07T21:01:00Z", raw_size: 1024, appended_uid: 0, appended_uid_validity: 0
  };
  vi.spyOn(api, "outbox").mockResolvedValue({ jobs: [job], summary });
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  try {
    await act(async () => root.render(createElement(OutboxView, {
      csrf: "csrf", summary, navigate: vi.fn(), addToast: vi.fn()
    })));
    expect(container.querySelector(".outbox-error strong")?.textContent).toBe("Message sent; Sent copy pending");
    expect(container.textContent).not.toContain("Rolltop could not finish this send");
    expect(container.textContent).toContain("1 SMTP attempt");
    expect(container.querySelectorAll(".outbox-steps .done")).toHaveLength(3);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  }
});
