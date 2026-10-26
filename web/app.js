// The page: cross-origin isolation (service worker on hosts that cannot
// send the headers), the terminal, and a minimal line editor that sends
// each line to the engine worker and prints what comes back. The engine
// filters escape sequences and bounds the output rate before anything
// reaches the terminal.
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

  const PROMPT = "\x1b[1;36m$\x1b[0m ";
  const state = { ready: false, running: null, line: "", history: [], hist: 0, nextId: 1, transcript: [], raw: "" };
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
        state.ready = true;
        say("engine " + m.version + " ready · images stored in " + m.storage);
        term.writeln("\x1b[1mwctr playground\x1b[0m: WebAssembly containers, in this tab. Type \x1b[1mhelp\x1b[0m.");
        term.write(PROMPT);
        break;
      case "out":
        if (state.running && m.id === state.running.id) {
          term.write(m.data);
          // What reached the terminal, for tests (bounded).
          state.raw = (state.raw + m.data).slice(-1 << 20);
        }
        break;
      case "done":
        if (!state.running || m.id !== state.running.id) {
          break;
        }
        state.transcript.push({ line: state.running.line, code: m.code, ms: performance.now() - state.running.t0 });
        state.running = null;
        if (m.clear) {
          term.clear();
        } else if (m.code !== 0) {
          term.writeln("\x1b[2m[exit status " + m.code + "]\x1b[0m");
        }
        term.write(PROMPT);
        break;
      case "fatal":
        say("engine failed: " + m.message);
        term.writeln("\r\nengine failed: " + m.message);
        break;
    }
  };
  engine.onerror = (e) => say("engine worker error: " + (e.message || e));

  function submit() {
    const line = state.line;
    state.line = "";
    term.write("\r\n");
    if (line.trim() !== "") {
      state.history.push(line);
      if (state.history.length > 200) state.history.shift();
    }
    state.hist = state.history.length;
    if (line.trim() === "") {
      term.write(PROMPT);
      return;
    }
    const id = state.nextId++;
    state.running = { id, line, t0: performance.now() };
    engine.postMessage({ t: "line", id, text: line });
  }

  function replaceLine(s) {
    term.write("\r\x1b[2K" + PROMPT + s);
    state.line = s;
  }

  term.onData((data) => {
    if (!state.ready) return;
    if (state.running) {
      if (data.includes("\x03")) {
        engine.postMessage({ t: "interrupt", id: state.running.id });
        term.write("^C");
      }
      return; // no stdin for containers yet
    }
    if (data === "\x1b[A") {
      if (state.hist > 0) replaceLine(state.history[--state.hist]);
      return;
    }
    if (data === "\x1b[B") {
      if (state.hist < state.history.length - 1) replaceLine(state.history[++state.hist]);
      else {
        state.hist = state.history.length;
        replaceLine("");
      }
      return;
    }
    if (data.startsWith("\x1b")) return; // other keys: no cursor editing yet
    for (const ch of data) {
      if (ch === "\r" || ch === "\n") {
        submit();
        if (state.running) return; // the rest of a paste waits for the prompt
      } else if (ch === "\x7f" || ch === "\b") {
        if (state.line.length > 0) {
          state.line = state.line.slice(0, -1);
          term.write("\b \b");
        }
      } else if (ch === "\x03") {
        state.line = "";
        term.write("^C\r\n" + PROMPT);
      } else if (ch === "\x0c") {
        term.clear();
        replaceLine(state.line);
      } else if (ch >= " " && state.line.length < 4096) {
        state.line += ch;
        term.write(ch);
      }
    }
  });
})();
