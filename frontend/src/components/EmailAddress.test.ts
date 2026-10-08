// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { EmailAddressText, EmailAddressWarning } from "./EmailAddress";

it("exposes the encoded form of a readable address without a warning", async () => {
  const container = document.createElement("div");
  const root = createRoot(container);
  try {
    await act(async () => root.render(createElement("div", null,
      createElement(EmailAddressText, { value: "support@xn--czasnacianie-slc.com" }),
      createElement(EmailAddressWarning, { value: "support@xn--czasnacianie-slc.com" })
    )));
    expect(container.textContent).toBe("support@czasnaścianie.com");
    expect(container.querySelector("bdi")?.title).toContain("support@xn--czasnacianie-slc.com");
    expect(container.querySelector("details")).toBeNull();
  } finally { await act(async () => root.unmount()); }
});

it("lets touch and keyboard users inspect both address forms without toggling the message", async () => {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const onClick = vi.fn();
  const onKeyDown = vi.fn();
  try {
    await act(async () => root.render(createElement("div", { onClick, onKeyDown },
      createElement(EmailAddressWarning, { value: "Support <support@аррӏе.com>" })
    )));
    const summary = container.querySelector("summary")!;
    expect(summary.textContent).toBe("Check address");
    await act(async () => summary.click());
    expect(container.querySelector("details")?.open).toBe(true);
    expect(container.textContent).toContain("support@аррӏе.com");
    expect(container.textContent).toContain("support@xn--80ak6aa92e.com");
    summary.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(onClick).not.toHaveBeenCalled();
    expect(onKeyDown).not.toHaveBeenCalled();
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});
