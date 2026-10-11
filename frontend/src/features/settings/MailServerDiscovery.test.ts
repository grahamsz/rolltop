// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { DiscoveredMailSettings } from "../../types";
import { MailServerDiscovery } from "./MailServerDiscovery";

let container: HTMLDivElement;
let root: Root;
const apply = vi.fn();
const settings: DiscoveredMailSettings = {
  imap: { host: "imap.example.com", port: 993, username: "alice", security: "tls", source: "Provider autoconfig" },
  smtp: { host: "smtp.example.com", port: 587, username: "alice@example.com", security: "starttls", source: "Microsoft Autodiscover" }
};

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  apply.mockClear();
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(email = "alice@example.com", kind: "imap" | "smtp" | "both" = "both") {
  await act(async () => root.render(createElement(MailServerDiscovery, { csrf: "csrf-token", email, kind, onApply: apply })));
}
async function click(label: string) {
  const button = Array.from(container.querySelectorAll("button")).find((button) => button.textContent === label);
  expect(button).toBeDefined();
  await act(async () => button!.click());
}

it("shows both sources and waits for explicit use before applying settings", async () => {
  const lookup = vi.spyOn(api, "discoverMailSettings").mockResolvedValue(settings);
  await render();
  await click("Find server settings");
  expect(lookup).toHaveBeenCalledExactlyOnceWith("csrf-token", "alice@example.com");
  expect(container.textContent).toContain("imap.example.com:993");
  expect(container.textContent).toContain("smtp.example.com:587");
  expect(container.textContent).toContain("Microsoft Autodiscover");
  expect(apply).not.toHaveBeenCalled();
  await click("Use these settings");
  expect(apply).toHaveBeenCalledExactlyOnceWith(settings);
  expect(container.textContent).toContain("Review them and save");
});

it("ignores a response for an email that has changed", async () => {
  let resolve!: (value: DiscoveredMailSettings) => void;
  vi.spyOn(api, "discoverMailSettings").mockImplementation(() => new Promise((done) => { resolve = done; }));
  await render();
  await click("Find server settings");
  await render("bob@example.com");
  await act(async () => resolve(settings));
  expect(container.textContent).not.toContain("imap.example.com");
  expect(container.textContent).not.toContain("Use these settings");
  expect(apply).not.toHaveBeenCalled();
});

it("normalizes international domains for DNS discovery", async () => {
  const lookup = vi.spyOn(api, "discoverMailSettings").mockResolvedValue({});
  await render("alice@bücher.example");
  await click("Find server settings");
  expect(lookup).toHaveBeenCalledExactlyOnceWith("csrf-token", "alice@xn--bcher-kva.example");
});

it("shows a manual fallback when discovery returns nothing", async () => {
  vi.spyOn(api, "discoverMailSettings").mockResolvedValue({});
  await render();
  await click("Find server settings");
  expect(container.textContent).toContain("enter the settings manually");
  expect(apply).not.toHaveBeenCalled();
});

it("allows retry after a lookup error", async () => {
  vi.spyOn(api, "discoverMailSettings").mockRejectedValueOnce(new Error("Lookup failed")).mockResolvedValueOnce(settings);
  await render();
  await click("Find server settings");
  expect(container.textContent).toContain("Lookup failed");
  await click("Find server settings");
  expect(container.textContent).toContain("Use these settings");
});

it("only applies SMTP settings in the outgoing editor", async () => {
  vi.spyOn(api, "discoverMailSettings").mockResolvedValue(settings);
  await render("alice@example.com", "smtp");
  await click("Find server settings");
  expect(container.textContent).not.toContain("Incoming IMAP");
  await click("Use these settings");
  expect(apply).toHaveBeenCalledExactlyOnceWith({ imap: undefined, smtp: settings.smtp });
});
