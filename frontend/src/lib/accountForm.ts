import type { Account } from "../types";

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
