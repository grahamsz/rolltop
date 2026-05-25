// File overview: Form adapters for account settings. They isolate UI defaults and account-to-form
// conversion from the large settings component.

import type { Account } from "../types";

/** emptyAccountForm returns UI defaults for a new IMAP account form. */
export function emptyAccountForm() {
  return {
    email: "",
    label: "",
    host: "",
    port: "993",
    username: "",
    password: "",
    use_tls: true,
    smtp_host: "",
    smtp_port: "587",
    smtp_username: "",
    smtp_password: "",
    smtp_use_tls: true,
    smtp_same_as_imap: true,
    mailbox: "*",
    sync_interval_minutes: "10"
  };
}

/** accountToForm adapts an API account row into editable string/boolean form state. */
export function accountToForm(account: Account | null) {
  if (!account) return emptyAccountForm();
  return {
    email: account.email || "",
    label: account.label || "",
    host: account.host || "",
    port: String(account.port || 993),
    username: account.username || "",
    password: "",
    use_tls: account.use_tls,
    smtp_host: account.smtp_host || "",
    smtp_port: String(account.smtp_port || 587),
    smtp_username: account.smtp_username || "",
    smtp_password: "",
    smtp_use_tls: account.smtp_use_tls,
    smtp_same_as_imap: account.smtp_same_as_imap,
    mailbox: account.mailbox || "*",
    sync_interval_minutes: String(account.sync_interval_minutes || 10)
  };
}
