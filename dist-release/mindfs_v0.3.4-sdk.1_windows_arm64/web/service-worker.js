const SHELL_CACHE = "mindfs-shell-324c730ef384";
const RUNTIME_CACHE = "mindfs-runtime-324c730ef384";
const OFFLINE_URL = new URL("./offline.html", self.location.href).toString();
const INDEX_URL = new URL("./index.html", self.location.href).toString();
const PRECACHE_URLS = [
  "./",
  "./index.html",
  "./apple-touch-icon.png",
  "./assets/agents/augment.svg",
  "./assets/agents/claude.svg",
  "./assets/agents/cline.svg",
  "./assets/agents/codex.svg",
  "./assets/agents/copilot.svg",
  "./assets/agents/cursor.svg",
  "./assets/agents/gemini.svg",
  "./assets/agents/hermes.webp",
  "./assets/agents/kimi.svg",
  "./assets/agents/kiro.svg",
  "./assets/agents/omp.svg",
  "./assets/agents/openclaw.svg",
  "./assets/agents/opencode.svg",
  "./assets/agents/pi.svg",
  "./assets/agents/qoder.svg",
  "./assets/agents/qwen.svg",
  "./favicon.svg",
  "./manifest.webmanifest",
  "./offline.html",
  "./pwa-192.png",
  "./pwa-512.png",
  "./pwa-icon-maskable.svg",
  "./pwa-icon.svg",
  "./pwa-maskable-192.png",
  "./pwa-maskable-512.png",
  "./assets/index-BS7RaQHc.js",
  "./assets/index-Dn8Ukci-.css",
  "./assets/index-MjJjYIvc.js",
  "./assets/index-YFjUWYkb.js",
  "./assets/index-f9aAWPMr.js"
];

function normalizedPathname(pathname) {
  const relayPrefixMatch = pathname.match(/^\/n\/[^/]+(?=\/|$)/);
  if (!relayPrefixMatch) {
    return pathname;
  }
  const normalized = pathname.slice(relayPrefixMatch[0].length);
  return normalized || "/";
}

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(SHELL_CACHE);
    await cache.addAll(PRECACHE_URLS.map((url) => new URL(url, self.location.href).toString()));
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const cacheKeys = await caches.keys();
    await Promise.all(cacheKeys
      .filter((key) => key !== SHELL_CACHE && key !== RUNTIME_CACHE)
      .map((key) => caches.delete(key)));
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") {
    return;
  }

  const url = new URL(request.url);
  if (url.origin !== self.location.origin) {
    return;
  }
  const pathname = normalizedPathname(url.pathname);
  if (pathname.startsWith("/mindfs-assets/")) {
    return;
  }
  if (pathname.startsWith("/api/") || pathname === "/api" || pathname === "/ws" || pathname === "/health") {
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(handleNavigationRequest(request));
    return;
  }

  event.respondWith(handleStaticRequest(request));
});

async function handleNavigationRequest(request) {
  const cache = await caches.open(SHELL_CACHE);
  try {
    const response = await fetch(request);
    cache.put(INDEX_URL, response.clone()).catch(() => {});
    return response;
  } catch {
    const cachedIndex = await cache.match(INDEX_URL);
    if (cachedIndex) {
      return cachedIndex;
    }
    const offlineResponse = await cache.match(OFFLINE_URL);
    if (offlineResponse) {
      return offlineResponse;
    }
    throw new Error("offline");
  }
}

async function handleStaticRequest(request) {
  const shellCache = await caches.open(SHELL_CACHE);
  const cachedShellResponse = await shellCache.match(request);
  if (cachedShellResponse) {
    return cachedShellResponse;
  }

  const runtimeCache = await caches.open(RUNTIME_CACHE);
  const cachedRuntimeResponse = await runtimeCache.match(request);
  if (cachedRuntimeResponse) {
    return cachedRuntimeResponse;
  }

  try {
    const response = await fetch(request);
    if (response.ok) {
      runtimeCache.put(request, response.clone()).catch(() => {});
    }
    return response;
  } catch {
    if (request.destination === "image") {
      const iconResponse = await shellCache.match(new URL("./pwa-192.png", self.location.href).toString());
      if (iconResponse) {
        return iconResponse;
      }
    }
    return new Response("", {
      status: 504,
      statusText: "Asset Unavailable",
    });
  }
}
