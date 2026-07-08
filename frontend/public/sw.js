const STATIC_CACHE = "rolltop-static-v6";
const MESSAGE_CACHE = "rolltop-notification-messages-v1";
const MESSAGE_CACHE_TTL_MS = 2 * 60 * 1000;
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
    caches.keys()
      .then((keys) => Promise.all(keys.filter((key) => key !== STATIC_CACHE && key !== MESSAGE_CACHE).map((key) => caches.delete(key))))
      .then(() => pruneNotificationMessageCache())
  );
  self.clients.claim();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin) return;

  if (url.pathname.startsWith("/brand-icons/") || url.pathname.startsWith("/plugins/")) return;

  if (isNotificationMessageRequest(req, url)) {
    event.respondWith(notificationMessageResponse(req));
    return;
  }

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
      url: sameOriginPath(data.url, "/mail"),
      apiURL: sameOriginPath(data.api_url, ""),
      messageID: Number(data.message_id || 0)
    }
  };
  event.waitUntil(Promise.all([
    warmNotificationMessage(options.data),
    self.registration.showNotification(title, options)
  ]));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  const targetURL = new URL(sameOriginPath(data.url, "/mail"), self.location.origin).href;
  event.waitUntil(
    Promise.all([
      warmNotificationMessage(data),
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
    ])
  );
});

function sameOriginPath(value, fallback) {
  if (typeof value !== "string" || !value) return fallback;
  try {
    const url = new URL(value, self.location.origin);
    if (url.origin !== self.location.origin) return fallback;
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return fallback;
  }
}

async function warmNotificationMessage(data) {
  const apiURL = sameOriginPath(data?.apiURL, "");
  if (!apiURL || !apiURL.startsWith("/api/messages/")) return;
  const url = new URL(apiURL, self.location.origin);
  if (!isNotificationMessageAPIURL(url)) return;
  try {
    const response = await fetch(apiURL, {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "include",
      cache: "reload"
    });
    if (!response.ok) return;
    await rememberNotificationMessage(apiURL, response);
    await pruneNotificationMessageCache();
  } catch {
    // Navigation should still work if the background warm-up misses.
  }
}

function isNotificationMessageRequest(req, url) {
  return req.method === "GET" && url.origin === self.location.origin && isNotificationMessageAPIURL(url);
}

function isNotificationMessageAPIURL(url) {
  return /^\/api\/messages\/\d+$/.test(url.pathname) && !url.search;
}

async function rememberNotificationMessage(apiURL, response) {
  const body = await response.clone().arrayBuffer();
  const headers = new Headers(response.headers);
  headers.set("X-Rolltop-Cached-At", String(Date.now()));
  headers.set("Cache-Control", "private, max-age=120");
  const cached = new Response(body, {
    status: response.status,
    statusText: response.statusText,
    headers
  });
  const cache = await caches.open(MESSAGE_CACHE);
  await cache.put(new Request(apiURL, { credentials: "include" }), cached);
}

async function notificationMessageResponse(req) {
  const cache = await caches.open(MESSAGE_CACHE);
  const cached = await cache.match(req);
  if (cached) {
    const cachedAt = Number(cached.headers.get("X-Rolltop-Cached-At") || 0);
    if (cachedAt && Date.now() - cachedAt <= MESSAGE_CACHE_TTL_MS) {
      await cache.delete(req);
      return cached;
    }
    await cache.delete(req);
  }
  return fetch(req);
}

async function pruneNotificationMessageCache() {
  const cache = await caches.open(MESSAGE_CACHE);
  const requests = await cache.keys();
  await Promise.all(requests.map(async (request) => {
    const cached = await cache.match(request);
    const cachedAt = Number(cached?.headers.get("X-Rolltop-Cached-At") || 0);
    if (!cachedAt || Date.now() - cachedAt > MESSAGE_CACHE_TTL_MS) {
      await cache.delete(request);
    }
  }));
}

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
