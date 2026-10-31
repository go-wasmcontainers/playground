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
  // Join class. The relay's origin (not a secret) is remembered in
  // localStorage; the engine worker is started with it in its URL, and the
  // service worker extends that worker's CSP connect-src to it, and only
  // it. The join code is typed into a password field, posted to the worker
  // once, and cleared: it is never stored, logged, put in a URL or in the
  // history. "Leave class" deletes the session token (IndexedDB) and the
  // remembered relay.
  const RKEY = "wctr-relay";
  const RELAY_RE = /^https:\/\/([a-z0-9.-]{1,253}|\[[0-9a-f:]{2,39}\])(:[0-9]{1,5})?$/;
  const relayOrigin = (s) => {
    try {
      const u = new URL(String(s).trim().replace(/^wss:\/\//i, "https://")); // wss://host names the same relay
      if (u.protocol !== "https:" || u.username || u.password || u.search || u.hash || (u.pathname !== "/" && u.pathname !== "")) return "";
      const o = "https://" + u.host.toLowerCase();
      return RELAY_RE.test(o) ? o : "";
    } catch (e) {
      return "";
    }
  };
  let relay = "";
  try {
    relay = relayOrigin(localStorage.getItem(RKEY) || "");
  } catch (e) {
    relay = "";
  }
  state.class = { state: "none", relay };
  const $ = (id) => document.getElementById(id);
  const dlg = $("joindlg");
  const showClass = () => {
    const c = state.class;
    const info = $("classinfo");
    $("joinbtn").hidden = c.state === "joined";
    if (c.state === "joined") {
      info.textContent = "class: you are " + c.label + (c.domain ? "." + c.domain : "") + " on " + c.relay + " (run --network class) ";
      const b = document.createElement("button");
      b.textContent = "Leave class";
      b.id = "leavebtn";
      b.addEventListener("click", () => engine && engine.postMessage({ t: "leave" }));
      info.appendChild(b);
      info.hidden = false;
    } else {
      info.hidden = true;
    }
  };
  const joinErr = (s) => {
    $("joinerr").textContent = s;
    $("joinerr").hidden = !s;
  };
  const openJoin = () => {
    joinErr("");
    $("joinstep1").hidden = !!relay;
    $("joinstep2").hidden = !relay;
    $("relayshown").textContent = relay;
    $("joinok").textContent = relay ? "Join" : "Continue";
    if (!dlg.open) dlg.showModal();
    (relay ? $("joinname") : $("relay")).focus();
  };
  $("joinbtn").addEventListener("click", openJoin);
  $("joincancel").addEventListener("click", () => {
    $("joincode").value = "";
    dlg.close();
  });
  $("relaychange").addEventListener("click", () => {
    try {
      localStorage.removeItem(RKEY);
    } catch (e) {}
    sessionStorage.setItem("wctr-join-open", "1");
    location.reload();
  });
  $("joinform").addEventListener("submit", (e) => {
    e.preventDefault();
    if (!relay) {
      const o = relayOrigin($("relay").value);
      if (!o) {
        joinErr("Enter the relay's address as https://host[:port] (https only).");
        return;
      }
      try {
        localStorage.setItem(RKEY, o);
      } catch (err) {
        joinErr("This browser does not let the page remember the relay.");
        return;
      }
      // Reopen the dialog after the reload, for the name and the code.
      sessionStorage.setItem("wctr-join-open", "1");
      location.reload();
      return;
    }
    const code = $("joincode").value;
    $("joincode").value = ""; // the code leaves the page's fields at once
    if (!engine || !state.ready) {
      joinErr("The engine is not running in this tab.");
      return;
    }
    joinErr("");
    $("joinok").disabled = true;
    engine.postMessage({ t: "join", code, name: $("joinname").value });
  });
  if (sessionStorage.getItem("wctr-join-open") === "1") {
    sessionStorage.removeItem("wctr-join-open");
    setTimeout(openJoin, 0);
  }
  let lastClass = null;
  const onClass = (m) => {
    lastClass = { t: "class", state: m.state, relay: m.relay, label: m.label, domain: m.domain, network: m.network, subnet: m.subnet, error: m.error };
    $("joinok").disabled = false;
    if (m.state === "joined") {
      state.class = { state: "joined", relay: m.relay, label: m.label, domain: m.domain, network: m.network, subnet: m.subnet };
      if (dlg.open) dlg.close();
    } else if (m.state === "left") {
      state.class = { state: "left", relay: "" };
      try {
        localStorage.removeItem(RKEY);
      } catch (e) {}
      relay = "";
      tell("You left the class. The page will no longer connect to that relay after a reload.", "OK", untell);
    } else {
      state.class = { state: "error", relay, error: m.error };
      if (dlg.open) {
        joinErr(m.error);
      } else {
        tell("Class: " + m.error, "Join again", () => {
          untell();
          openJoin();
        });
      }
    }
    showClass();
  };
  showClass();
  // The dashboard's side of "Join class" (go-wasmcontainers/dashboard
  // v0.5.0, README "Join class"): it reads the relay, asks for another one
  // through useRelay (stored, then the page reloads so the engine worker
  // starts with that origin in its CSP; "" forgets it), reads the worker's
  // last {t:"class"} report through status(), and posts {t:"join", code,
  // name} and {t:"leave"} to window.__wctrEngine itself.
  window.__wctrClass = {
    relay,
    useRelay(origin) {
      const o = origin ? relayOrigin(origin) : "";
      if (origin && !o) {
        throw new Error("a relay is https://host[:port]");
      }
      try {
        if (o) {
          localStorage.setItem(RKEY, o);
        } else {
          localStorage.removeItem(RKEY);
        }
      } catch (e) {
        throw new Error("this browser does not let the page remember the relay");
      }
      location.reload();
    },
    status() {
      return lastClass;
    },
  };

  function start() {
    state.owner = true;
    untell();
    term.focus(); // after "Use it here", focus was on the (now hidden) button
    // ?codecache=1 on the page turns the compiled-module cache on (off by
    // default: measured, it does not pay; see cmd/engine).
    const cc = /[?&]codecache=1\b/.test(location.search) ? "&codecache=1" : "";
    const rp = relay ? "&relay=" + encodeURIComponent(relay) : "";
    engine = new Worker("engine-worker.js?v=" + build + cc + rp);
    window.__wctrEngine = engine;
    window.__wctrBuild = build;
    // The dashboard's bridge listens on the worker too (addEventListener);
    // its http.* messages are not this page's.
    window.wctrPorts.attach(engine);
    engine.onmessage = (e) => {
      const m = e.data;
      if (!m || typeof m.t !== "string" || m.t.startsWith("http.")) return;
      if (m.t === "ports" || m.t.startsWith("port-")) {
        window.wctrPorts.onEngine(m); // published ports (ports.js)
        return;
      }
      switch (m.t) {
        case "ready":
          say("engine " + m.version + " ready · images and containers stored in " + m.storage);
          state.restored = { restored: m.restored, stopped: m.stopped, repaired: m.repaired };
          state.engineReadyMs = performance.now() - state.t0;
          state.ready = true;
          window.wctrPorts.refresh(); // restored containers may publish ports
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
        case "class":
          onClass(m);
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
        case "frame":
          // A GPU surface's frame: the dashboard draws and acknowledges it;
          // without one, nothing shows it, so it is closed and acknowledged
          // here, and the guest is not slowed waiting for the page.
          state.frames = (state.frames || 0) + 1;
          if (!dash) {
            // Tests look at the frames through this hook (gpu_test.go).
            if (typeof window.__wctrFrameProbe === "function") {
              try {
                window.__wctrFrameProbe(m);
              } catch (e) {
                // a probe's failure is the test's to report
              }
            }
            if (m.bitmap && typeof m.bitmap.close === "function") m.bitmap.close();
            engine.postMessage({ t: "frame-ack", surface: m.surface, seq: m.seq });
          }
          break;
        case "compile":
          state.compiles.push({ source: m.source, bytes: m.bytes, ms: m.ms, compileMs: m.compileMs, error: m.error });
          break;
        case "done":
          state.transcript.push({ line: m.line, code: m.code, ms: m.ms });
          window.wctrPorts.refresh(); // a command may have published or freed a port
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
