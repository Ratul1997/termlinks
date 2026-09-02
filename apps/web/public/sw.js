const CACHE_NAME = "termlinks-app-shell-v14";
const APP_SHELL = [
  "/",
  "/index.html",
  "/assets/main.css",
  "/assets/main.js",
  "/manifest.webmanifest",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/icon-maskable-512.png",
  "/icons/apple-touch-icon.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE_NAME).then((cache) => cache.addAll(APP_SHELL)));
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((names) => Promise.all(names.filter((name) => name !== CACHE_NAME).map((name) => caches.delete(name))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith("/api/") || url.pathname.startsWith("/ws/")) return;

  if (request.mode === "navigate") {
    event.respondWith(networkWithCacheFallback(request, "/index.html"));
    return;
  }
  if (url.pathname.startsWith("/assets/") || url.pathname.startsWith("/icons/") || url.pathname === "/manifest.webmanifest") {
    event.respondWith(networkWithCacheFallback(request));
  }
});

async function networkWithCacheFallback(request, fallback) {
  const cache = await caches.open(CACHE_NAME);
  try {
    const response = await fetch(request);
    if (response.ok && response.type === "basic") await cache.put(request, response.clone());
    return response;
  } catch {
    const cached = await cache.match(request) || (fallback ? await cache.match(fallback) : undefined);
    return cached || Response.error();
  }
}
