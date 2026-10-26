// Cross-origin isolation for GitHub Pages, which cannot send headers: this
// service worker re-serves every same-origin response with COOP/COEP/CORP
// (SharedArrayBuffer and shared WebAssembly memories need them) and the
// page's Content-Security-Policy (workers take their CSP from their own
// response, not from the page's meta tag). The approach is
// coi-serviceworker's (MIT, github.com/gzuidhof/coi-serviceworker),
// reduced to what this page needs: same-origin only, require-corp.
"use strict";

const CSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
  "connect-src 'self'; worker-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (new URL(req.url).origin !== self.location.origin) {
    return; // never touch another origin (the CSP forbids it anyway)
  }
  if (req.cache === "only-if-cached" && req.mode !== "same-origin") {
    return;
  }
  e.respondWith(
    fetch(req).then((res) => {
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
    }),
  );
});
