import type { Bootstrap } from "../types";

export function embeddedBootstrap(root: Document = document): Bootstrap | null {
  const element = root.getElementById("rolltop-startup");
  if (!element?.textContent) return null;
  try {
    const value = JSON.parse(element.textContent) as unknown;
    if (!validBootstrap(value)) return null;
    return value;
  } catch {
    return null;
  }
}

function validBootstrap(value: unknown): value is Bootstrap {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const bootstrap = value as Record<string, unknown>;
  if (typeof bootstrap.users_exist !== "boolean" || typeof bootstrap.csrf !== "string") return false;
  if (!Array.isArray(bootstrap.mailboxes)) return false;
  if (bootstrap.user === null) return true;
  if (!bootstrap.user || typeof bootstrap.user !== "object" || Array.isArray(bootstrap.user)) return false;
  const user = bootstrap.user as Record<string, unknown>;
  return typeof user.id === "number" && Number.isSafeInteger(user.id) && user.id > 0 &&
    typeof user.email === "string" && typeof user.name === "string";
}
