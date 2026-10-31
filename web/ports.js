// Published ports (`wctr run -p 8080:80 …`) in the browser: the playground
// tab's side. A bar lists the ports the engine has published; opening one
// asks the engine for a session (internal/portrelay) and opens the view tab
// (view.html) with it. The view relays the requests of the container's page
// over a BroadcastChannel; this tab hands them to the engine and the
// answers back -- for the sessions it opened only, so that two playground
// tabs never answer for each other.
"use strict";
window.wctrPorts = (() => {
  // ?usercontent=http://localhost:PORT on the page, for a local build or a
  // test; the engine accepts that or the default origin, nothing else.
  const uc = new URLSearchParams(location.search).get("usercontent") || "";
  const mine = new Set(); // session ids this tab opened
  const opening = new Map(); // request id -> the window opened for it
  const state = { ports: [], views: [], error: "" };
  let engine = null;
  let rid = 0;
  const ch = typeof BroadcastChannel === "function" ? new BroadcastChannel("wctr-ports") : null;

  function bar() {
    return document.getElementById("portbar");
  }

  function draw(ports) {
    state.ports = ports;
    const b = bar();
    if (!b) {
      return;
    }
    b.replaceChildren();
    b.hidden = ports.length === 0;
    if (ports.length === 0) {
      return;
    }
    b.append("Published: ");
    for (const p of ports) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = "open :" + p;
      btn.addEventListener("click", () => open(p));
      // A WebDAV server (the fileshare image): the go-fileshare explorer.
      const ex = document.createElement("button");
      ex.type = "button";
      ex.textContent = "explore :" + p;
      ex.addEventListener("click", () => window.wctrExplore && window.wctrExplore.ask(p));
      b.append(btn, " ", ex, " ");
    }
  }

  // open runs in the click: a tab opened later, when the engine answers,
  // would be a blocked popup. It starts blank and is sent to the view.
  function open(port) {
    if (!engine) {
      return;
    }
    const w = window.open("about:blank", "_blank");
    const r = "o" + ++rid;
    opening.set(r, w);
    engine.postMessage({ t: "port-open", rid: r, port, usercontent: uc });
  }

  function onEngine(m) {
    // The explorer's session and its answers are its own (explore.js).
    if (window.wctrExplore && window.wctrExplore.owns(m)) {
      window.wctrExplore.onEngine(m);
      return;
    }
    switch (m.t) {
      case "ports":
        draw(Array.isArray(m.items) ? m.items.filter((p) => Number.isInteger(p)) : []);
        return;
      case "port-opened": {
        const w = opening.get(m.rid);
        opening.delete(m.rid);
        if (m.error) {
          state.error = m.error;
          if (w) w.close();
          const b = bar();
          if (b) b.append(" " + m.error);
          return;
        }
        mine.add(m.sid);
        const u = "view.html?s=" + encodeURIComponent(m.sid) + "&p=" + m.port + "&uc=" + encodeURIComponent(m.usercontent) + "#" + m.nonce;
        state.views.push({ sid: m.sid, port: m.port });
        if (w) {
          w.opener = null; // the view needs nothing of this tab but the channel
          w.location.replace(new URL(u, location.href).href);
        }
        return;
      }
    }
    // port-head, port-chunk, port-end, port-error: to the view.
    if (ch && mine.has(m.sid)) {
      ch.postMessage(m);
    }
  }

  if (ch) {
    ch.onmessage = (e) => {
      const m = e.data;
      if (!m || !engine || !mine.has(m.sid)) {
        return;
      }
      if (m.t === "req") {
        const body = m.body instanceof ArrayBuffer ? m.body : null;
        engine.postMessage({ t: "port-req", sid: m.sid, uc: uc || "https://go-wasmcontainers-usercontent.github.io", id: m.id,
          port: m.port, method: m.method, path: m.path, headers: m.headers, body }, body ? [body] : []);
      } else if (m.t === "abort") {
        engine.postMessage({ t: "port-abort", sid: m.sid, id: m.id });
      } else if (m.t === "close") {
        mine.delete(m.sid);
        engine.postMessage({ t: "port-close", sid: m.sid });
      }
    };
  }

  return {
    state,
    attach(w) {
      engine = w;
    },
    engine() {
      return engine;
    },
    refresh() {
      if (engine) engine.postMessage({ t: "ports", rid: "list" });
    },
    onEngine,
    open,
  };
})();
