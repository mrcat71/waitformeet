/// <reference lib="webworker" />

/**
 * Service worker.
 *
 * Its only job is making the site installable and survivable on a flaky mobile
 * connection: static assets are cached, and the last successful render of the main
 * page is kept so an offline visit still shows a countdown rather than a browser
 * error. Nothing private is cached, and anything under /admin, /media or /api is
 * deliberately never stored.
 */

export {};

// The webworker lib types the global as WorkerGlobalScope, which lacks skipWaiting,
// clients and the service-worker event map. Narrowing through a local alias is the
// supported way to get those types without redeclaring the global itself.
const sw = self as unknown as ServiceWorkerGlobalScope;

const VERSION = "v1";
const STATIC_CACHE = `wfm-static-${VERSION}`;
const PAGE_CACHE = `wfm-page-${VERSION}`;

/** Paths whose responses must never be written to a cache on disk. */
const NEVER_CACHE = ["/admin", "/media", "/api", "/auth", "/login", "/logout", "/notes", "/gallery"];

function isCacheable(url: URL): boolean {
  if (url.origin !== sw.location.origin) {
    return false;
  }
  return !NEVER_CACHE.some((prefix) => url.pathname === prefix || url.pathname.startsWith(`${prefix}/`));
}

sw.addEventListener("install", (event) => {
  // Take over as soon as the new worker is ready rather than waiting for every
  // tab to close, so a deploy is not stuck behind an open phone browser.
  event.waitUntil(sw.skipWaiting());
});

sw.addEventListener("activate", (event) => {
  event.waitUntil(
    (async (): Promise<void> => {
      const keep = new Set([STATIC_CACHE, PAGE_CACHE]);
      const names = await caches.keys();
      await Promise.all(names.filter((name) => !keep.has(name)).map((name) => caches.delete(name)));
      await sw.clients.claim();
    })(),
  );
});

/** Cache-first for immutable static assets. */
async function staticFirst(request: Request): Promise<Response> {
  const cache = await caches.open(STATIC_CACHE);
  const hit = await cache.match(request);
  if (hit) {
    return hit;
  }
  const response = await fetch(request);
  if (response.ok) {
    await cache.put(request, response.clone());
  }
  return response;
}

/** Network-first for pages, falling back to the last good copy when offline. */
async function networkFirst(request: Request): Promise<Response> {
  const cache = await caches.open(PAGE_CACHE);
  try {
    const response = await fetch(request);
    if (response.ok) {
      await cache.put(request, response.clone());
    }
    return response;
  } catch (err) {
    const hit = await cache.match(request);
    if (hit) {
      return hit;
    }
    throw err;
  }
}

sw.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") {
    return;
  }

  const url = new URL(request.url);
  if (!isCacheable(url)) {
    return;
  }

  if (url.pathname.startsWith("/static/")) {
    event.respondWith(staticFirst(request));
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(networkFirst(request));
  }
});
