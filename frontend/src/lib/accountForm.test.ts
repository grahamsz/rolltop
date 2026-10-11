import { expect, it } from "vitest";
import { applyDiscoveredMailSettings, emptyAccountForm } from "./accountForm";

it("preserves credentials and sync preferences while applying separate discovered servers", () => {
  const form = { ...emptyAccountForm(), email: "alice@example.com", label: "Work", password: "private-password", smtp_password: "private-smtp-password", mailbox: "INBOX", sync_interval_minutes: "20" };
  const next = applyDiscoveredMailSettings(form, {
    imap: { host: "imap.example.com", port: 993, username: "alice", security: "tls", source: "Provider autoconfig" },
    smtp: { host: "submission.example.com", port: 465, username: "outgoing-alice", security: "tls", source: "Microsoft Autodiscover" }
  });
  expect(next).toMatchObject({ host: "imap.example.com", port: "993", username: "alice", use_tls: true, smtp_host: "submission.example.com", smtp_port: "465", smtp_username: "outgoing-alice", smtp_same_as_imap: false, smtp_use_tls: true });
  for (const key of ["password", "smtp_password", "label", "email", "mailbox", "sync_interval_minutes"] as const) expect(next[key]).toBe(form[key]);
  expect(form.host).toBe("");
});

it("keeps manual SMTP settings when only IMAP is discovered", () => {
  const form = { ...emptyAccountForm(), smtp_same_as_imap: false, smtp_host: "manual.example.com", smtp_port: "465", smtp_username: "manual" };
  const next = applyDiscoveredMailSettings(form, { imap: { host: "imap.example.com", port: 993, username: "alice", security: "tls", source: "DNS SRV" } });
  expect(next.smtp_host).toBe(form.smtp_host);
  expect(next.smtp_port).toBe(form.smtp_port);
  expect(next.smtp_username).toBe(form.smtp_username);
});

it("keeps derived SMTP settings consistent when only IMAP is discovered", () => {
  const next = applyDiscoveredMailSettings(emptyAccountForm(), { imap: { host: "imap.example.com", port: 993, username: "alice", security: "tls", source: "DNS SRV" } });
  expect(next.smtp_host).toBe("imap.example.com");
  expect(next.smtp_username).toBe("alice");
  expect(next.smtp_same_as_imap).toBe(true);
});
