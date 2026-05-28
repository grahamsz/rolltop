const STATIC_CACHE = "rolltop-static-v4";
const STATIC_ASSETS = ["/", "/mail", "/manifest.webmanifest", "/icon.svg", "/icon.svg?v=transparent-logo-v2"];
let pgpUnlockUserID = 0;
let pgpUnlockState = { unlockedUntil: 0, keys: [] };
let pgpUnlockTimer = 0;

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(STATIC_CACHE).then((cache) => cache.addAll(STATIC_ASSETS)));
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((key) => key !== STATIC_CACHE).map((key) => caches.delete(key)))
    )
  );
  self.clients.claim();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin) return;

  if (url.pathname.startsWith("/brand-icons/") || url.pathname.startsWith("/plugins/")) return;

  if (url.pathname.startsWith("/api/")) return;

  event.respondWith(
    fetch(req)
      .then((res) => {
        if (res.ok) caches.open(STATIC_CACHE).then((cache) => cache.put(req, res.clone()));
        return res;
      })
      .catch(() => caches.match(req).then((cached) => cached || caches.match("/mail")))
  );
});

self.addEventListener("message", (event) => {
  const data = event.data || {};
  if (data.type === "rolltop:pgp-unlock-get") {
    const userID = Number(data.userID || 0);
    const state = currentPGPUnlockState(userID);
    if (!state.unlockedUntil) requestPGPUnlockStateFromClients(userID);
    event.source?.postMessage({ type: "rolltop:pgp-unlock-state", userID, state });
    return;
  }
  if (data.type !== "rolltop:pgp-unlock-set") return;
  const userID = Number(data.userID || 0);
  const state = normalizePGPUnlockState(data.state);
  pgpUnlockUserID = userID;
  pgpUnlockState = state;
  schedulePGPLock();
  broadcastPGPUnlockState(userID);
});

function normalizePGPUnlockState(state) {
  const unlockedUntil = Number(state?.unlockedUntil || 0);
  if (!unlockedUntil || unlockedUntil <= Date.now()) return { unlockedUntil: 0, keys: [] };
  const keys = Array.isArray(state?.keys) ? state.keys.filter((key) => key && key.private_key_armored) : [];
  return keys.length > 0 ? { unlockedUntil, keys } : { unlockedUntil: 0, keys: [] };
}

function currentPGPUnlockState(userID) {
  if (userID !== pgpUnlockUserID) return { unlockedUntil: 0, keys: [] };
  pgpUnlockState = normalizePGPUnlockState(pgpUnlockState);
  if (!pgpUnlockState.unlockedUntil) pgpUnlockUserID = 0;
  return pgpUnlockState;
}

function schedulePGPLock() {
  if (pgpUnlockTimer) clearTimeout(pgpUnlockTimer);
  pgpUnlockTimer = 0;
  if (!pgpUnlockState.unlockedUntil) return;
  pgpUnlockTimer = setTimeout(() => {
    const userID = pgpUnlockUserID;
    pgpUnlockUserID = 0;
    pgpUnlockState = { unlockedUntil: 0, keys: [] };
    broadcastPGPUnlockState(userID);
  }, Math.max(0, pgpUnlockState.unlockedUntil - Date.now()));
}

async function broadcastPGPUnlockState(userID) {
  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const state = currentPGPUnlockState(userID);
  clients.forEach((client) => client.postMessage({ type: "rolltop:pgp-unlock-state", userID, state }));
}

async function requestPGPUnlockStateFromClients(userID) {
  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  clients.forEach((client) => client.postMessage({ type: "rolltop:pgp-unlock-request", userID }));
}
