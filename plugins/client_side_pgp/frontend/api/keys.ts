import { deleteJSON, getJSON, postJSON } from "../../../../frontend/src/api";
import type { ContactPGPKey, IdentityPGPPrivateKey } from "../../../../frontend/src/types";

const pluginAPIBase = "/api/plugins/client_side_pgp";

export function pgpPrivateKeys() {
  return getJSON<{ keys: IdentityPGPPrivateKey[] }>(`${pluginAPIBase}/private-keys`);
}

export function savePGPPrivateKey(csrf: string, key: IdentityPGPPrivateKey) {
  return postJSON<{ ok: boolean; key: IdentityPGPPrivateKey }>(`${pluginAPIBase}/private-keys`, csrf, key);
}

export function deletePGPPrivateKey(csrf: string, id: number) {
  return deleteJSON<{ ok: boolean }>(`${pluginAPIBase}/private-keys/${id}`, csrf);
}

export function pgpPublicKeys(emails: string[], all = false) {
  const q = new URLSearchParams();
  emails.forEach((email) => q.append("email", email));
  if (all) q.set("all", "1");
  return getJSON<{ keys: ContactPGPKey[] }>(`${pluginAPIBase}/public-keys?${q}`);
}

export function savePGPPublicKey(csrf: string, key: ContactPGPKey) {
  return postJSON<{ ok: boolean; key: ContactPGPKey }>(`${pluginAPIBase}/public-keys`, csrf, key);
}
