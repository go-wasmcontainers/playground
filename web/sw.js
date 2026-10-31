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

// The engine worker started for a class (engine-worker.js?relay=https://
// host[:port]) may connect to that one relay, https and wss; everything
// else keeps 'self'. The same rule as internal/site CSPFor (tested there):
// the value is spliced into the header, so only a plain host passes.
const RELAY_RE = /^https:\/\/([a-z0-9.-]{1,253}|\[[0-9a-f:]{2,39}\])(:[0-9]{1,5})?$/;
function cspFor(url) {
  if (url.pathname.endsWith("/engine-worker.js")) {
    const r = url.searchParams.get("relay");
    if (r && RELAY_RE.test(r)) {
      const host = r.slice("https://".length);
      return CSP.replace("connect-src 'self'", "connect-src 'self' https://" + host + " wss://" + host);
    }
  }
  return CSP;
}

// The view tab (view.html) embeds the usercontent shell, of another site,
// which sends no CORP: it is not isolated (no COEP), and nothing may embed
// it. The same rule as internal/site ViewCSP (tested there).
const VIEW_CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
  "frame-src https://go-wasmcontainers-usercontent.github.io http://localhost:* http://127.0.0.1:*; " +
  "object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
const isView = (url) => url.pathname.endsWith("/view.html");

function viewHeaders(res) {
  if (res.status === 0) {
    return res;
  }
  const h = new Headers(res.headers);
  h.set("Cross-Origin-Opener-Policy", "same-origin");
  h.delete("Cross-Origin-Embedder-Policy");
  h.set("Content-Security-Policy", VIEW_CSP);
  h.set("X-Content-Type-Options", "nosniff");
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers: h });
}

function isolate(res, csp) {
  if (res.status === 0) {
    return res;
  }
  const h = new Headers(res.headers);
  h.set("Cross-Origin-Opener-Policy", "same-origin");
  h.set("Cross-Origin-Embedder-Policy", "require-corp");
  h.set("Cross-Origin-Resource-Policy", "same-origin");
  h.set("Content-Security-Policy", csp || CSP);
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
          await cache.put(shellURL(p), p === "view.html" ? viewHeaders(res) : isolate(res));
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
      // The view tab: this build's copy, whatever the query, with its own
      // headers (not the playground's page, which every other navigation is).
      if (isView(url)) {
        const hit = await cache.match(shellURL("view.html"));
        return hit || viewHeaders(await fetch(req));
      }
      // The page itself: this build's copy, whatever the query.
      let key = req.url;
      if (req.mode === "navigate" || url.pathname === new URL(self.registration.scope).pathname ||
        url.pathname.endsWith("/index.html")) {
        key = shellURL("");
      }
      // The engine worker for a class: the shell's copy, with its CSP.
      const csp = cspFor(url);
      if (csp !== CSP) {
        const k = new URL(url);
        k.searchParams.delete("relay");
        k.searchParams.delete("codecache");
        const hit = await cache.match(k.href);
        if (hit) {
          return isolate(hit, csp);
        }
        return isolate(await fetch(req), csp);
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
