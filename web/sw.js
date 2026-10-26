// The playground's service worker, two jobs:
//
// 1. Cross-origin isolation for GitHub Pages, which cannot send headers:
//    every same-origin response is re-served with COOP/COEP/CORP (for
//    SharedArrayBuffer and shared WebAssembly memories) and the page's
//    Content-Security-Policy (workers take their CSP from their own
//    response). The approach is coi-serviceworker's (MIT,
//    github.com/gzuidhof/coi-serviceworker), reduced to same-origin only.
//
// 2. The app shell, offline: this build's files are kept in Cache Storage
//    ("shell-<build>") and served from there, so a reload works without
//    network and loads nothing twice. A new build ships as a new sw.js
//    (byte-different: the build id below), which installs its own shell
//    beside the old one and then WAITS: the page says an update is
//    available, and switches only when asked (postMessage "activate"), so a
//    page never runs with half its files from one build and half from the
//    next. The build's file list and id are written into this file at build
//    time.
"use strict";

const BUILD = "{{BUILD}}";
const SHELL = JSON.parse('{{SHELL}}'); // paths relative to this worker
const CACHE = "shell-" + BUILD;

const CSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
  "connect-src 'self'; worker-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";

function isolate(res) {
  if (res.status === 0) {
    return res;
  }
  const h = new Headers(res.headers);
  h.set("Cross-Origin-Opener-Policy", "same-origin");
  h.set("Cross-Origin-Embedder-Policy", "require-corp");
  h.set("Cross-Origin-Resource-Policy", "same-origin");
  h.set("Content-Security-Policy", CSP);
  h.set("X-Content-Type-Options", "nosniff");
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers: h });
}

// The URL a file is requested with: assets carry ?v=<build>, the page not.
function shellURL(p) {
  const u = new URL(p, self.registration.scope);
  if (p !== "" && p !== "index.html") {
    u.search = "?v=" + BUILD;
  }
  return u.href;
}

self.addEventListener("install", (e) => {
  e.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE);
      // Fetched fresh (not from the HTTP cache), each checked as it lands:
      // a failed file fails the install, and the old build keeps serving.
      await Promise.all(
        SHELL.map(async (p) => {
          const res = await fetch(shellURL(p), { cache: "no-store" });
          if (!res.ok) {
            throw new Error(p + ": HTTP " + res.status);
          }
          await cache.put(shellURL(p), isolate(res));
        }),
      );
      // The very first worker has no page running an older build: it takes
      // over at once (the page needs it for isolation). Later ones wait.
      if (!self.registration.active) {
        await self.skipWaiting();
      }
    })(),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    (async () => {
      for (const k of await caches.keys()) {
        if (k.startsWith("shell-") && k !== CACHE) {
          await caches.delete(k); // older builds; wctr-modules is the engine's
        }
      }
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (e) => {
  if (e.data === "activate") {
    self.skipWaiting();
  } else if (e.data === "build") {
    e.source.postMessage({ build: BUILD });
  }
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) {
    return; // never touch another origin (the CSP forbids it anyway)
  }
  if (req.cache === "only-if-cached" && req.mode !== "same-origin") {
    return;
  }
  if (req.method !== "GET") {
    e.respondWith(fetch(req).then(isolate));
    return;
  }
  e.respondWith(
    (async () => {
      const cache = await caches.open(CACHE);
      // The page itself: this build's copy, whatever the query.
      let key = req.url;
      if (req.mode === "navigate" || url.pathname === new URL(self.registration.scope).pathname ||
        url.pathname.endsWith("/index.html")) {
        key = shellURL("");
      }
      const hit = (await cache.match(key)) || (await cache.match(key.replace(/\/$/, "/index.html")));
      if (hit) {
        return hit;
      }
      // Everything else (images, catalog, build.json): the network, and
      // the shell's copy when offline.
      try {
        return isolate(await fetch(req));
      } catch (err) {
        const stale = await cache.match(req.url, { ignoreSearch: true });
        if (stale) {
          return stale;
        }
        throw err;
      }
    })(),
  );
});
