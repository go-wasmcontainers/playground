// The page: cross-origin isolation (service worker on hosts that cannot
// send the headers) and the terminal, a thin adapter: keys go to the engine
// worker raw, whose Go console (the engine's cli/lineedit) edits, completes
// and runs the line, and its output is written back. The engine filters
// escape sequences and bounds the output rate before anything reaches the
// terminal.
"use strict";

(function () {
  const el = document.getElementById("terminal");
  const status = document.getElementById("status");
  const build = el.dataset.build;
  const say = (s) => (status.textContent = s);

  if (!window.crossOriginIsolated) {
    if (!("serviceWorker" in navigator)) {
      say("this browser cannot isolate the page (no service worker): containers need SharedArrayBuffer");
      return;
    }
    // One reload after the worker takes control; never a loop.
    const tried = sessionStorage.getItem("coi-reload");
    navigator.serviceWorker.register("sw.js").then(
      () => {
        if (!tried) {
          sessionStorage.setItem("coi-reload", "1");
          if (navigator.serviceWorker.controller) {
            location.reload();
          } else {
            navigator.serviceWorker.addEventListener("controllerchange", () => location.reload());
          }
        } else {
          say("cross-origin isolation failed: containers need SharedArrayBuffer");
        }
      },
      (err) => say("service worker registration failed: " + err),
    );
    return;
  }
  sessionStorage.removeItem("coi-reload");

  const term = new Terminal({
    convertEol: true,
    cursorBlink: true,
    scrollback: 5000,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    fontSize: 14,
    theme: { background: "#0f1419", foreground: "#e6e1cf" },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(el);
  fit.fit();
  window.addEventListener("resize", () => fit.fit());
  term.focus();

  // Line editing, history and completion are the engine's (Go, cli/lineedit):
  // this page forwards keys raw and writes what comes back.
  const HKEY = "wctr-history";
  let saved = [];
  try {
    const h = JSON.parse(sessionStorage.getItem(HKEY) || "[]");
    if (Array.isArray(h)) saved = h.filter((s) => typeof s === "string").slice(-500);
  } catch (e) {
    saved = [];
  }
  const state = { ready: false, transcript: [], raw: "" };
  // For tests: the terminal's visible text, and whether a command runs.
  window.__playground = {
    state,
    text() {
      const b = term.buffer.active;
      const lines = [];
      for (let i = 0; i < b.length; i++) {
        lines.push(b.getLine(i).translateToString(true));
      }
      return lines.join("\n").replace(/\s+$/, "");
    },
  };

  const engine = new Worker("engine-worker.js?v=" + build);
  engine.onmessage = (e) => {
    const m = e.data;
    switch (m.t) {
      case "ready":
        say("engine " + m.version + " ready · images stored in " + m.storage);
        engine.postMessage({ t: "init", history: saved, cols: term.cols });
        state.ready = true;
        break;
      case "write":
        term.write(m.data);
        // What reached the terminal, for tests (bounded).
        state.raw = (state.raw + m.data).slice(-1 << 20);
        break;
      case "history":
        try {
          sessionStorage.setItem(HKEY, JSON.stringify(m.items));
        } catch (e) {
          // full or disabled storage: history stays for this page only
        }
        break;
      case "done":
        state.transcript.push({ line: m.line, code: m.code, ms: m.ms });
        break;
      case "fatal":
        say("engine failed: " + m.message);
        term.writeln("\r\nengine failed: " + m.message);
        break;
    }
  };
  engine.onerror = (e) => say("engine worker error: " + (e.message || e));

  term.onData((data) => {
    if (state.ready) engine.postMessage({ t: "input", data });
  });
  term.onResize(({ cols }) => {
    if (state.ready) engine.postMessage({ t: "resize", cols });
  });
})();
