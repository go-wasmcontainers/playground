// The view tab: a container's published port, shown on the usercontent
// origin. It embeds that origin's shell (internal/usercontent), whose
// service worker turns each request of the container's page into a message;
// this tab hands them to the playground tab over a BroadcastChannel, where
// the engine relays them to the container (internal/portrelay), and hands
// the answers back to the shell.
//
// The playground opened this tab with view.html?s=<session>&p=<port>&uc=
// <usercontent origin>#<nonce>. The session is the engine's; the nonce goes
// to the shell, with every message, and leaves this URL at once.
"use strict";
(() => {
  const DEFAULT_UC = "https://go-wasmcontainers-usercontent.github.io";
  const ID = /^[A-Za-z0-9_-]{22,64}$/;
  const LOCAL_UC = /^http:\/\/(localhost|127\.0\.0\.1):[0-9]{1,5}$/;
  const $ = (id) => document.getElementById(id);
  const state = { ready: false, reqs: 0, answers: 0, error: "" };
  window.__wctrView = state; // for tests

  function fail(msg) {
    state.error = msg;
    $("status").textContent = msg;
    $("status").classList.add("error");
  }

  const q = new URLSearchParams(location.search);
  const sid = q.get("s") || "";
  const port = Number(q.get("p"));
  const uc = q.get("uc") || DEFAULT_UC;
  const nonce = location.hash.slice(1);
  history.replaceState(null, "", location.pathname + location.search); // the nonce leaves the URL
  if (!ID.test(sid) || !ID.test(nonce) || !Number.isInteger(port) || port < 1 || port > 65535 ||
    (uc !== DEFAULT_UC && !LOCAL_UC.test(uc))) {
    fail("This link is not a port the playground opened. Open the port again from the playground.");
    return;
  }
  if (typeof BroadcastChannel !== "function") {
    fail("This browser has no BroadcastChannel: the view cannot reach the playground.");
    return;
  }
  $("what").textContent = "port " + port;
  document.title = "wctr: port " + port;

  const ch = new BroadcastChannel("wctr-ports");
  const frame = document.createElement("iframe");
  frame.title = "the container's page on port " + port;
  frame.src = uc + "/shell.html?s=" + encodeURIComponent(sid) + "#" + nonce;
  $("main").appendChild(frame);

  // From the shell: ready, then the requests of the container's page.
  window.addEventListener("message", (e) => {
    if (e.origin !== uc || e.source !== frame.contentWindow) {
      return;
    }
    const m = e.data;
    if (!m || m.sid !== sid) {
      return;
    }
    if (m.t === "ready") {
      state.ready = true;
      $("status").textContent = "served by the playground tab, from the engine in it";
      frame.contentWindow.postMessage({ t: "open", nonce, port, path: "/" }, uc);
    } else if (m.t === "req" && ID.test(m.id || "")) {
      state.reqs++;
      ch.postMessage({ t: "req", sid, id: m.id, port: m.port, method: m.method, path: m.path, headers: m.headers, body: m.body || null });
    } else if (m.t === "abort" && ID.test(m.id || "")) {
      ch.postMessage({ t: "abort", sid, id: m.id });
    }
  });

  // From the playground tab: the answers, for this session only.
  ch.onmessage = (e) => {
    const m = e.data;
    if (!m || m.sid !== sid || !ID.test(m.id || "")) {
      return;
    }
    const w = frame.contentWindow;
    switch (m.t) {
      case "port-head":
        state.answers++;
        w.postMessage({ t: "head", nonce, id: m.id, status: m.status, headers: m.headers }, uc);
        break;
      case "port-chunk":
        if (m.data instanceof ArrayBuffer) {
          w.postMessage({ t: "chunk", nonce, id: m.id, data: m.data }, uc, [m.data]);
        }
        break;
      case "port-end":
        w.postMessage({ t: "end", nonce, id: m.id }, uc);
        break;
      case "port-error":
        state.answers++;
        w.postMessage({ t: "error", nonce, id: m.id, status: m.status === 503 ? 503 : 502 }, uc);
        break;
    }
  };

  // Closing this tab ends the session in the engine.
  window.addEventListener("pagehide", () => ch.postMessage({ t: "close", sid }));
})();
