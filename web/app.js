// The page: cross-origin isolation (a service worker on hosts that cannot
// send the headers, which also keeps the app shell offline), one engine per
// browser (a Web Lock: a second tab offers to take over), and one of two
// UIs over the engine worker:
//
//   - the dashboard (default): dashboard.wasm on the #screen canvas, which
//     speaks the engine's API to the worker (window.__wctrEngine) over
//     postMessage (the dashboard's bridge); its Terminal runs wctr;
//   - ?ui=terminal: xterm.js as a thin adapter, keys sent raw to the
//     worker's Go console (the engine's cli/lineedit), output written back.
//
// The engine filters escape sequences and bounds the output rate before
// anything reaches either terminal.
"use strict";

(function () {
  const el = document.getElementById("terminal");
  const status = document.getElementById("status");
  const notice = document.getElementById("notice");
  const build = el.dataset.build;
  const dash = !/[?&]ui=terminal\b/.test(location.search);
  if (dash) {
    document.body.classList.add("dash");
  } else {
    const sw = document.getElementById("switch");
    sw.textContent = "dashboard";
    sw.href = "./";
  }
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
    document.body.classList.add("noticed");
  };
  const untell = () => {
    notice.hidden = true;
    document.body.classList.remove("noticed");
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

  const state = { ready: false, owner: false, transcript: [], raw: "", compiles: [], restored: null, persisted: null, update: false, build,
    ui: dash ? "dashboard" : "terminal", dashboard: false, t0: performance.now(), engineReadyMs: 0, dashboardReadyMs: 0 };

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

  // The xterm.js terminal, in ?ui=terminal only; in dashboard mode a stub
  // that writes nowhere.
  let term = { cols: 80, focus() {}, write() {}, writeln() {}, onData() {}, onResize() {}, buffer: { active: { length: 0 } } };
  if (!dash) {
    term = new Terminal({
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
  }

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

  // dashboard.wasm downloads and compiles while the engine worker starts;
  // it runs once the engine is ready.
  const dashModule = dash ? WebAssembly.compileStreaming(fetch("dashboard.wasm?v=" + build)) : null;
  if (dashModule) dashModule.catch(() => {}); // reported when it is awaited
  let engine = null;
  let persistAsked = false;
  const askPersist = () => {
    // Once something is stored, ask the browser not to evict it (Chromium
    // and Safari decide silently, Firefox asks).
    if (persistAsked || !navigator.storage || !navigator.storage.persist) return;
    persistAsked = true;
    navigator.storage.persist().then((p) => (state.persisted = p), () => {});
  };
  // The dashboard: dashboard.wasm in this page, once per page (a takeover
  // restarts the engine, and the dashboard reconnects to the new worker
  // through window.__wctrEngine on the next page load).
  function runDashboard() {
    if (state.dashboard) return;
    state.dashboard = true;
    say("starting the dashboard…");
    const go = new Go();
    dashModule.then((m) => WebAssembly.instantiate(m, go.importObject)).then(
      (instance) => {
        const screen = document.getElementById("screen");
        screen.focus();
        // The first click is a user gesture: the moment to ask for storage.
        screen.addEventListener("pointerdown", askPersist, { once: true });
        document.body.classList.add("running");
        go.run(instance);
        const t = setInterval(() => {
          if (window.__dashboard) {
            clearInterval(t);
            state.dashboardReadyMs = performance.now() - state.t0;
          }
        }, 20);
      },
      (err) => say("the dashboard failed to load: " + err),
    );
  }
  function start() {
    state.owner = true;
    untell();
    term.focus(); // after "Use it here", focus was on the (now hidden) button
    // ?codecache=1 on the page turns the compiled-module cache on (off by
    // default: measured, it does not pay; see cmd/engine).
    const cc = /[?&]codecache=1\b/.test(location.search) ? "&codecache=1" : "";
    engine = new Worker("engine-worker.js?v=" + build + cc);
    window.__wctrEngine = engine;
    window.__wctrBuild = build;
    // The dashboard's bridge listens on the worker too (addEventListener);
    // its http.* messages are not this page's.
    engine.onmessage = (e) => {
      const m = e.data;
      if (!m || typeof m.t !== "string" || m.t.startsWith("http.")) return;
      switch (m.t) {
        case "ready":
          say("engine " + m.version + " ready · images and containers stored in " + m.storage);
          state.restored = { restored: m.restored, stopped: m.stopped, repaired: m.repaired };
          state.engineReadyMs = performance.now() - state.t0;
          state.ready = true;
          if (dash) {
            if (m.restored > 0 || m.repaired > 0) {
              let n = "Restored " + m.restored + " container(s) from your last visit";
              if (m.stopped > 0) n += "; " + m.stopped + " were running and are now exited (137)";
              if (m.repaired > 0) n += "; " + m.repaired + " damaged record(s) dropped";
              tell(n + ".", "OK", untell);
            }
            runDashboard();
          } else {
            engine.postMessage({ t: "init", history: saved, cols: term.cols });
          }
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
          if (m.code === 0 && /^\s*wctr\s+(pull|run)\b/.test(m.line)) askPersist();
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
    untell();
    if (dash && state.dashboard) {
      // This page's dashboard is bound to the worker that stopped (Go
      // cannot be run twice in one page): a fresh page takes over instead.
      sessionStorage.setItem(STEAL, "1");
      location.reload();
      return;
    }
    own({ steal: true });
  }
  const STEAL = "wctr-take-over";
  if (navigator.locks) {
    const steal = sessionStorage.getItem(STEAL) === "1";
    sessionStorage.removeItem(STEAL);
    own(steal ? { steal: true } : { ifAvailable: true });
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
