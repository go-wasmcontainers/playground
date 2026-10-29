// The shell of a usercontent origin: it shows one published port of a
// playground session, served by this origin's service worker (sw.js), whose
// requests it relays to the view tab that embeds it -- the playground's
// own page -- and whose answers it hands back.
//
// It talks to exactly one origin, ALLOW (the playground, written in at
// build time), and accepts from it only messages that carry the session's
// nonce, which only that view knows: it came in this page's URL fragment.
// It refuses to run anywhere but inside that origin.
//
// What it is not: a boundary against the container's own pages. They are
// on this origin too and can script this page; what they reach through it
// is what the view relays -- HTTP to this session's published ports, bounded
// and checked -- and nothing of the playground itself.
"use strict";
(() => {
  const ALLOW = "{{ALLOW}}";
  const ID = /^[A-Za-z0-9_-]{22,64}$/;
  const MAX_PENDING = 256;

  const sid = new URLSearchParams(location.search).get("s") || "";
  const nonce = location.hash.slice(1);

  function refuse(why) {
    document.body.textContent = "This page shows a container's port, inside the playground (" + ALLOW + "). Refused: " + why;
    throw new Error(why);
  }
  if (!ID.test(sid) || !ID.test(nonce)) {
    refuse("no session");
  }
  // The fragment is read: it leaves the URL, so nothing on this origin
  // reads it again from location or history.
  history.replaceState(null, "", location.pathname + location.search);
  if (window.parent === window) {
    refuse("it is not embedded");
  }
  // frame-ancestors cannot be sent by a host without headers on the first
  // load, so the embedder is checked here: ancestorOrigins where the
  // browser has it, the referrer's origin otherwise.
  const anc = location.ancestorOrigins;
  if (anc && anc.length) {
    if (anc.length !== 1 || anc[0] !== ALLOW) {
      refuse("embedded by " + anc[anc.length - 1]);
    }
  } else {
    let ref = "";
    try {
      ref = new URL(document.referrer).origin;
    } catch (e) {}
    if (ref !== ALLOW) {
      refuse("embedded by another page");
    }
  }

  const pending = new Map(); // request id -> the service worker's port
  const frame = document.createElement("iframe");
  frame.title = "container";

  // From the service worker: a request of the container's page.
  navigator.serviceWorker.addEventListener("message", (e) => {
    const m = e.data;
    const port = e.ports && e.ports[0];
    if (!port) {
      return;
    }
    if (!m || m.t !== "req" || m.sid !== sid || !ID.test(m.id || "") || pending.has(m.id) || pending.size >= MAX_PENDING) {
      port.postMessage({ t: "error", status: 503 });
      return;
    }
    pending.set(m.id, port);
    const transfer = m.body instanceof ArrayBuffer ? [m.body] : [];
    parent.postMessage({ t: "req", sid, id: m.id, port: m.port, method: m.method, path: m.path, headers: m.headers, body: m.body || null }, ALLOW, transfer);
  });

  // From the view: open a port, or an answer.
  window.addEventListener("message", (e) => {
    if (e.origin !== ALLOW || e.source !== window.parent) {
      return;
    }
    const m = e.data;
    if (!m || m.nonce !== nonce) {
      return;
    }
    if (m.t === "open") {
      const port = Number(m.port);
      const path = typeof m.path === "string" ? m.path : "/";
      if (!Number.isInteger(port) || port < 1 || port > 65535 || path[0] !== "/" || path.length > 8192) {
        return;
      }
      frame.src = "/s/" + sid + "/" + port + path;
      if (!frame.isConnected) {
        document.body.appendChild(frame);
      }
      return;
    }
    const port = pending.get(m.id);
    if (!port) {
      return;
    }
    if (m.t === "head") {
      port.postMessage({ t: "head", status: m.status, headers: m.headers });
    } else if (m.t === "chunk" && m.data instanceof ArrayBuffer) {
      port.postMessage({ t: "chunk", data: m.data }, [m.data]);
    } else if (m.t === "end" || m.t === "error") {
      port.postMessage({ t: m.t, status: m.status });
      pending.delete(m.id);
    }
  });

  (async () => {
    await navigator.serviceWorker.register("sw.js", { scope: "./" });
    await navigator.serviceWorker.ready;
    parent.postMessage({ t: "ready", sid }, ALLOW);
  })().catch((err) => refuse("the service worker: " + err));
})();
