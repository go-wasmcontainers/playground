// The page: cross-origin isolation (a service worker on hosts that cannot
// send the headers, which also keeps the app shell offline), one engine per
// browser (a Web Lock: a second tab offers to take over), and the
// terminal, a thin adapter: keys go to the engine worker raw, whose Go
// console (the engine's cli/lineedit) edits, completes and runs the line,
// and its output is written back. The engine filters escape sequences and
// bounds the output rate before anything reaches the terminal.
"use strict";

(function () {
  const el = document.getElementById("terminal");
  const status = document.getElementById("status");
  const notice = document.getElementById("notice");
  const build = el.dataset.build;
  const say = (s) => (status.textContent = s);
  // A line in the header with one action, or none.
  const tell = (text, label, action) => {
    notice.textContent = text + " ";
    if (label) {
      const b = document.createElement("button");
      b.textContent = label;
      b.addEventListener("click", action);
      notice.appendChild(b);
    }
    notice.hidden = false;
  };

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

  const state = { ready: false, owner: false, transcript: [], raw: "", compiles: [], restored: null, persisted: null, update: false, build };

  // Updates: a new build installs beside this one and waits; switching is
  // the user's choice, and reloads the page onto the new build whole.
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("sw.js").then((reg) => {
      let switching = false;
      const offer = () => {
        if (!reg.waiting || !navigator.serviceWorker.controller) return;
        state.update = true;
        tell("A new version of the playground is ready.", "Reload to update", () => {
          switching = true;
          reg.waiting.postMessage("activate");
        });
      };
      navigator.serviceWorker.addEventListener("controllerchange", () => {
        if (switching) location.reload();
      });
      reg.addEventListener("updatefound", () => {
        const w = reg.installing;
        if (w) w.addEventListener("statechange", () => w.state === "installed" && offer());
      });
      offer();
      window.__playground.checkUpdate = () => reg.update().then(offer);
    }, () => {});
  }

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
  // For tests: the terminal's visible text, and what the engine reported.
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

  let engine = null;
  let persistAsked = false;
  function start() {
    state.owner = true;
    notice.hidden = true;
    // ?codecache=1 on the page turns the compiled-module cache on (off by
    // default: measured, it does not pay; see cmd/engine).
    const cc = /[?&]codecache=1\b/.test(location.search) ? "&codecache=1" : "";
    engine = new Worker("engine-worker.js?v=" + build + cc);
    engine.onmessage = (e) => {
      const m = e.data;
      switch (m.t) {
        case "ready":
          say("engine " + m.version + " ready · images and containers stored in " + m.storage);
          state.restored = { restored: m.restored, stopped: m.stopped, repaired: m.repaired };
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
        case "compile":
          state.compiles.push({ source: m.source, bytes: m.bytes, ms: m.ms, compileMs: m.compileMs, error: m.error });
          break;
        case "done":
          state.transcript.push({ line: m.line, code: m.code, ms: m.ms });
          // Once something is stored, ask the browser not to evict it
          // (Chromium and Safari decide silently, Firefox asks).
          if (!persistAsked && m.code === 0 && /^\s*wctr\s+(pull|run)\b/.test(m.line) && navigator.storage && navigator.storage.persist) {
            persistAsked = true;
            navigator.storage.persist().then((p) => (state.persisted = p), () => {});
          }
          break;
        case "fatal":
          say("engine failed: " + m.message);
          term.writeln("\r\nengine failed: " + m.message);
          break;
      }
    };
    engine.onerror = (e) => say("engine worker error: " + (e.message || e));
  }
  function stop(why) {
    if (engine) engine.terminate();
    engine = null;
    state.ready = false;
    state.owner = false;
    say(why);
    term.writeln("\r\n\x1b[2m" + why + "\x1b[0m");
    tell(why, "Use it here", takeOver);
  }

  // One engine per browser: two engines writing one IndexedDB would
  // overwrite each other's containers. The lock is held for the page's
  // life; a second tab waits, or takes it over (the first then stops).
  const held = () => new Promise(() => {});
  function own(options) {
    return navigator.locks.request("wctr-engine", options, (lock) => {
      if (!lock) {
        say("the playground is open in another tab");
        tell("The playground is already running in another tab of this browser.", "Use it here", takeOver);
        return;
      }
      start();
      return held();
    }).catch((err) => {
      if (err && err.name === "AbortError") stop("Another tab took over the playground; this one has stopped.");
    });
  }
  function takeOver() {
    notice.hidden = true;
    own({ steal: true });
  }
  if (navigator.locks) {
    own({ ifAvailable: true });
  } else {
    start(); // no Web Locks: one engine per tab, as before
  }

  term.onData((data) => {
    if (state.ready && engine) engine.postMessage({ t: "input", data });
  });
  term.onResize(({ cols }) => {
    if (state.ready && engine) engine.postMessage({ t: "resize", cols });
  });
})();
