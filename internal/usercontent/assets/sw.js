// The service worker of a usercontent origin. Everything under
// /s/<session>/<port>/ is a container's port: each request becomes a
// message to the shell of that session (shell.js), which the playground
// answers; nothing is cached, here or in the browser. Everything else is
// this origin's few static files.
//
// The playground's origin (ALLOW) is written in at build time; it is the
// only page allowed to embed the shell, which this worker also says in a
// header once it is installed (a host without headers cannot, the first
// time).
"use strict";

const ALLOW = "{{ALLOW}}";
const ID = /^[A-Za-z0-9_-]{22,64}$/;
const ROUTE = /^\/s\/([A-Za-z0-9_-]{22,64})\/([0-9]{1,5})(\/.*)?$/;
const METHODS = new Set(["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]);
const MAX_BODY = 16 << 20;
const MAX_RESPONSE = 256 << 20;
const HEAD_TIMEOUT = 60000;
// Defence in depth: the playground filters a container's answer
// (internal/portroute); these are dropped here again.
const DROP = new Set(["set-cookie", "clear-site-data", "service-worker-allowed", "connection", "keep-alive",
  "transfer-encoding", "upgrade", "content-length", "cache-control", "refresh"]);

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("fetch", (e) => {
  const u = new URL(e.request.url);
  if (u.origin !== self.location.origin) {
    return;
  }
  if (u.pathname === "/shell.html") {
    e.respondWith(fetch(e.request, { cache: "no-store" }).then((res) => withHeaders(res, {
      "Content-Security-Policy": "frame-ancestors " + ALLOW,
      "Cache-Control": "no-store",
    })));
    return;
  }
  const m = ROUTE.exec(u.pathname);
  if (!m) {
    return; // this origin's own files
  }
  e.respondWith(proxy(e.request, m[1], Number(m[2]), (m[3] || "/") + u.search));
});

function withHeaders(res, add) {
  const h = new Headers(res.headers);
  for (const [k, v] of Object.entries(add)) {
    h.set(k, v);
  }
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers: h });
}

function page(status, text) {
  return new Response(text + "\n", {
    status,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
  });
}

function randomID() {
  const b = new Uint8Array(18);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_");
}

async function shellOf(sid) {
  for (const c of await self.clients.matchAll({ type: "window", includeUncontrolled: true })) {
    const u = new URL(c.url);
    if (u.pathname === "/shell.html" && u.searchParams.get("s") === sid) {
      return c;
    }
  }
  return null;
}

async function proxy(req, sid, port, path) {
  if (!ID.test(sid) || port < 1 || port > 65535 || path.length > 8192) {
    return page(400, "Bad port address.");
  }
  if (!METHODS.has(req.method)) {
    return page(405, "Method not relayed.");
  }
  const shell = await shellOf(sid);
  if (!shell) {
    return page(503, "This port is shown through the playground (" + ALLOW + "): open it from there, in the tab that runs the containers.");
  }
  let body = null;
  if (req.method !== "GET" && req.method !== "HEAD") {
    if (Number(req.headers.get("content-length") || 0) > MAX_BODY) {
      return page(413, "Request body too large.");
    }
    body = await req.arrayBuffer();
    if (body.byteLength > MAX_BODY) {
      return page(413, "Request body too large.");
    }
  }
  const headers = [];
  req.headers.forEach((v, k) => headers.push([k, v]));
  const id = randomID();
  const ch = new MessageChannel();
  return new Promise((resolve) => {
    let ctrl = null;
    let total = 0;
    let settled = false;
    const done = () => ch.port1.close();
    const timer = setTimeout(() => {
      if (!settled) {
        settled = true;
        resolve(page(504, "The container did not answer."));
        done();
      }
    }, HEAD_TIMEOUT);
    ch.port1.onmessage = (e) => {
      const m = e.data || {};
      if (m.t === "head" && !settled) {
        settled = true;
        clearTimeout(timer);
        const status = Number(m.status);
        if (!Number.isInteger(status) || status < 200 || status > 599) {
          resolve(page(502, "The container's answer was not valid."));
          done();
          return;
        }
        const h = new Headers();
        for (const kv of Array.isArray(m.headers) ? m.headers.slice(0, 200) : []) {
          if (Array.isArray(kv) && typeof kv[0] === "string" && typeof kv[1] === "string" && !DROP.has(kv[0].toLowerCase())) {
            try {
              h.append(kv[0], kv[1]);
            } catch (err) {} // a field the browser refuses is left out
          }
        }
        h.set("Cache-Control", "no-store");
        h.set("X-Content-Type-Options", "nosniff");
        const empty = req.method === "HEAD" || status === 204 || status === 205 || status === 304;
        const stream = empty ? null : new ReadableStream({
          start(c) { ctrl = c; },
          cancel() { done(); },
        });
        resolve(new Response(stream, { status, headers: h }));
        if (empty) {
          done();
        }
      } else if (m.t === "chunk" && ctrl && m.data instanceof ArrayBuffer) {
        total += m.data.byteLength;
        if (total > MAX_RESPONSE) {
          ctrl.error(new Error("response too large"));
          done();
          return;
        }
        ctrl.enqueue(new Uint8Array(m.data));
      } else if (m.t === "end") {
        if (ctrl) {
          ctrl.close();
        }
        done();
      } else if (m.t === "error") {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          resolve(page(m.status === 503 ? 503 : 502, "The container could not be reached."));
        } else if (ctrl) {
          ctrl.error(new Error("the container's answer broke off"));
        }
        done();
      }
    };
    const msg = { t: "req", sid, id, port, method: req.method, path, headers, body };
    shell.postMessage(msg, body ? [ch.port2, body] : [ch.port2]);
  });
}
