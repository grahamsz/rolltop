const STATIC_CACHE = "rolltop-static-v5";
const STATIC_ASSETS = ["/", "/mail", "/manifest.webmanifest", "/icon.svg", "/icon.svg?v=transparent-logo-v2"];
let securityUnlockUserID = 0;
let securityUnlockState = { unlockedUntil: 0, keys: [] };
let securityUnlockTimer = 0;

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

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  const title = typeof data.title === "string" && data.title ? data.title : "rolltop";
  const icon = typeof data.icon === "string" && data.icon ? data.icon : "/icon.svg?v=transparent-logo-v2";
  const options = {
    body: typeof data.body === "string" ? data.body : "New mail synced.",
    tag: typeof data.tag === "string" && data.tag ? data.tag : "rolltop-new-mail",
    icon,
    badge: typeof data.badge === "string" && data.badge ? data.badge : icon,
    data: {
      url: typeof data.url === "string" && data.url ? data.url : "/mail"
    }
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const targetURL = new URL(event.notification.data?.url || "/mail", self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(async (clients) => {
      for (const client of clients) {
        if (!client.url.startsWith(self.location.origin)) continue;
        if ("navigate" in client && client.url !== targetURL) {
          const navigated = await client.navigate(targetURL);
          return navigated?.focus();
        }
        return client.focus();
      }
      return self.clients.openWindow(targetURL);
    })
  );
});

self.addEventListener("message", (event) => {
  const data = event.data || {};
  if (data.type === "rolltop:security-unlock-get") {
    const userID = Number(data.userID || 0);
    const state = currentSecurityUnlockState(userID);
    if (!state.unlockedUntil) requestSecurityUnlockStateFromClients(userID);
    event.source?.postMessage({ type: "rolltop:security-unlock-state", userID, state });
    return;
  }
  if (data.type !== "rolltop:security-unlock-set") return;
  const userID = Number(data.userID || 0);
  const state = normalizeSecurityUnlockState(data.state);
  securityUnlockUserID = userID;
  securityUnlockState = state;
  scheduleSecurityLock();
  broadcastSecurityUnlockState(userID);
});

function normalizeSecurityUnlockState(state) {
  const unlockedUntil = Number(state?.unlockedUntil || 0);
  if (!unlockedUntil || unlockedUntil <= Date.now()) return { unlockedUntil: 0, keys: [] };
  const keys = Array.isArray(state?.keys) ? state.keys.filter((key) => key && key.private_key_armored) : [];
  return keys.length > 0 ? { unlockedUntil, keys } : { unlockedUntil: 0, keys: [] };
}

function currentSecurityUnlockState(userID) {
  if (userID !== securityUnlockUserID) return { unlockedUntil: 0, keys: [] };
  securityUnlockState = normalizeSecurityUnlockState(securityUnlockState);
  if (!securityUnlockState.unlockedUntil) securityUnlockUserID = 0;
  return securityUnlockState;
}

function scheduleSecurityLock() {
  if (securityUnlockTimer) clearTimeout(securityUnlockTimer);
  securityUnlockTimer = 0;
  if (!securityUnlockState.unlockedUntil) return;
  securityUnlockTimer = setTimeout(() => {
    const userID = securityUnlockUserID;
    securityUnlockUserID = 0;
    securityUnlockState = { unlockedUntil: 0, keys: [] };
    broadcastSecurityUnlockState(userID);
  }, Math.max(0, securityUnlockState.unlockedUntil - Date.now()));
}

async function broadcastSecurityUnlockState(userID) {
  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const state = currentSecurityUnlockState(userID);
  clients.forEach((client) => client.postMessage({ type: "rolltop:security-unlock-state", userID, state }));
}

async function requestSecurityUnlockStateFromClients(userID) {
  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  clients.forEach((client) => client.postMessage({ type: "rolltop:security-unlock-request", userID }));
}
