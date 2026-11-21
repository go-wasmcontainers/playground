// Sound for containers run with --audio (go-wasmcontainers/engine
// docs/audio-design.md, section 5): the page's side of the host module
// wctr_audio_out.
//
// The engine worker allocates one SharedArrayBuffer ring per stream and posts
//   {t:"audio", op:"open",  stream, container, sampleRate, channels, capacityFrames, sab}
//   {t:"audio", op:"close", stream}
// The page keeps ONE AudioContext, loads the worklet (audio-worklet.js: the
// embedded asset of wasmbox's audioring module, byte for byte), and gives each
// stream an AudioWorkletNode that reads the ring on the audio thread:
//
//   node -> container gain -> page gain -> destination
//   node -> analyser -> silent gain -> destination        (test tap, before any gain)
//
// A context is born suspended (Web Audio 1.1, "allowed to start"): capturing
// pointerdown and keydown listeners on window resume() it, stay until it
// runs, and come back when it falls back to suspended or interrupted (Safari).
// Until then the worklet does not run, the ring's heartbeat stands still and
// the engine discards what the guest writes: sound made before the click is
// never played late.
//
// Volume and mute live at two levels: one pair per container (the dashboard's
// pane calls setContainer(container, volume, muted); no controls of their own
// here) and the page-wide mute (the Sound strip's button). They are gain
// nodes, so a guest cannot undo them. window.__wctrAudio is the test hook.
"use strict";
window.wctrAudio = (() => {
  const MAX_STREAMS = 8; // per page, as the engine's module
  const state = { context: "none", streams: 0 };
  const streams = new Map(); // id -> stream
  const rows = new Map(); // container -> { gain, volume, muted, ids:Set, el parts }
  const prefs = new Map(); // container -> { volume, muted }: kept across runs of a container
  let ctx = null;
  let pageGain = null;
  let outTap = null; // analyser after the page gain (tests)
  let ready = null; // the worklet module's promise
  let globalMuted = false;
  let armed = false;
  let bar = null;

  const isInt = (v) => typeof v === "number" && Number.isInteger(v);

  // validate reads an untrusted open message once and returns what it will use.
  function validate(m) {
    if (!isInt(m.stream) || m.stream < 1 || streams.has(m.stream)) return null;
    if (typeof m.container !== "string" || m.container.length > 256) return null;
    const rate = m.sampleRate, ch = m.channels, cap = m.capacityFrames, sab = m.sab;
    if (!isInt(rate) || rate < 8000 || rate > 48000) return null;
    if (!isInt(ch) || ch < 1 || ch > 2) return null;
    if (!isInt(cap) || cap < 256 || cap > 16384 || (cap & (cap - 1)) !== 0) return null;
    if (typeof SharedArrayBuffer === "undefined" || !(sab instanceof SharedArrayBuffer)) return null;
    if (sab.byteLength !== 64 + cap * ch * 4) return null;
    if (streams.size >= MAX_STREAMS) return null;
    return { id: m.stream, container: m.container, rate, ch, cap, sab };
  }

  function setState() {
    state.context = ctx ? ctx.state : "none";
  }

  // The unlock: a user gesture is what lets a context leave "suspended".
  function onGesture() {
    if (ctx && ctx.state !== "running") ctx.resume().catch(() => {});
  }
  function arm() {
    if (armed) return;
    armed = true;
    window.addEventListener("pointerdown", onGesture, true);
    window.addEventListener("keydown", onGesture, true);
  }
  function disarm() {
    if (!armed) return;
    armed = false;
    window.removeEventListener("pointerdown", onGesture, true);
    window.removeEventListener("keydown", onGesture, true);
  }

  function context() {
    if (ctx) return ctx;
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return null;
    ctx = new AC({ latencyHint: "interactive" });
    pageGain = ctx.createGain();
    pageGain.gain.value = globalMuted ? 0 : 1;
    pageGain.connect(ctx.destination);
    // The test tap after both gain levels: what the speakers get.
    outTap = ctx.createAnalyser();
    outTap.fftSize = 4096;
    const outSilent = ctx.createGain();
    outSilent.gain.value = 0;
    pageGain.connect(outTap);
    outTap.connect(outSilent);
    outSilent.connect(ctx.destination);
    const t =document.getElementById("terminal");
    ready = ctx.audioWorklet.addModule("audio-worklet.js?v=" + encodeURIComponent((t && t.dataset.build) || ""));
    ready.catch(() => {}); // reported where it is awaited
    ctx.addEventListener("statechange", () => {
      setState();
      if (ctx.state === "running") {
        disarm();
      } else {
        arm(); // suspended again, or interrupted (Safari): wait for the next gesture
      }
    });
    setState();
    if (ctx.state !== "running") arm();
    return ctx;
  }

  // ---- the Sound strip: the page-wide mute (per container is the dashboard pane's) ----
  function ensureBar() {
    if (bar) return bar;
    bar = document.createElement("div");
    bar.id = "soundbar";
    bar.setAttribute("role", "group");
    bar.setAttribute("aria-label", "Sound");
    const head = document.createElement("div");
    head.className = "soundhead";
    head.append("Sound ");
    const g = document.createElement("button");
    g.type = "button";
    g.id = "soundmute";
    g.setAttribute("aria-pressed", "false");
    g.textContent = "mute all";
    g.addEventListener("click", () => setGlobalMute(!globalMuted));
    head.append(g);
    bar.append(head);
    document.body.append(bar);
    return bar;
  }

  function setGlobalMute(on) {
    globalMuted = !!on;
    if (pageGain) pageGain.gain.value = globalMuted ? 0 : 1;
    const g = document.getElementById("soundmute");
    if (g) {
      g.setAttribute("aria-pressed", String(globalMuted));
      g.textContent = globalMuted ? "unmute all" : "mute all";
    }
  }

  function apply(row) {
    row.gain.gain.value = row.muted ? 0 : row.volume;
  }

  // A container's gain node. Its volume and mute are set by the dashboard's
  // pane (setContainer); the level is remembered by container, so a pane that
  // sets it before the stream opens, or a container that is run again, keeps it.
  function rowFor(name) {
    let row = rows.get(name);
    if (row) return row;
    const p = prefs.get(name) || { volume: 1, muted: false };
    row = { name, volume: p.volume, muted: p.muted, ids: new Set(), gain: ctx.createGain() };
    row.gain.connect(pageGain);
    rows.set(name, row);
    apply(row);
    ensureBar();
    return row;
  }

  function dropRow(row) {
    row.gain.disconnect();
    rows.delete(row.name);
    if (rows.size === 0 && bar) {
      bar.remove();
      bar = null;
    }
  }

  // setContainer is the dashboard pane's entry point (dashboard host
  // ConnectSurfaces): volume 0..1 and mute for the sound of one container.
  function setContainer(name, volume, muted) {
    if (typeof name !== "string" || typeof volume !== "number" || !Number.isFinite(volume)) return;
    const p = { volume: Math.min(1, Math.max(0, volume)), muted: !!muted };
    prefs.set(name, p);
    const row = rows.get(name);
    if (row) {
      row.volume = p.volume;
      row.muted = p.muted;
      apply(row);
    }
  }

  // ---- streams ----
  async function open(m) {
    const v = validate(m);
    if (!v) return;
    const c = context();
    if (!c) return;
    const s = { ...v, hdr: new Int32Array(v.sab, 0, 16), node: null, closed: false, analyser: null, tap: null };
    streams.set(v.id, s);
    state.streams = streams.size;
    try {
      await ready;
    } catch (e) {
      streams.delete(v.id);
      state.streams = streams.size;
      return;
    }
    if (s.closed) return; // closed while the module loaded
    const row = rowFor(v.container);
    row.ids.add(v.id);
    s.row = row;
    s.node = new AudioWorkletNode(c, "wasmbox-ring", {
      numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [2],
      processorOptions: { sab: v.sab, sampleRate: v.rate, channels: v.ch, capacityFrames: v.cap },
    });
    s.node.connect(row.gain);
    // The test tap, before the gains: what the worklet produced.
    s.analyser = c.createAnalyser();
    s.analyser.fftSize = 4096;
    s.analyser.smoothingTimeConstant = 0;
    s.tap = c.createGain();
    s.tap.gain.value = 0;
    s.node.connect(s.analyser);
    s.analyser.connect(s.tap);
    s.tap.connect(c.destination);
  }

  function close(m) {
    const s = streams.get(m.stream);
    if (!s) return;
    s.closed = true;
    streams.delete(s.id);
    state.streams = streams.size;
    if (s.node) {
      s.node.disconnect();
      s.analyser.disconnect();
      s.tap.disconnect();
    }
    if (s.row) {
      s.row.ids.delete(s.id);
      if (s.row.ids.size === 0) dropRow(s.row);
    }
  }

  function onEngine(m) {
    if (m.op === "open") open(m);
    else if (m.op === "close") close(m);
  }

  // ---- the test hook ----
  const fbuf = { t: null, f: null };
  function analyse(s) {
    const a = s.analyser;
    if (!fbuf.t || fbuf.t.length !== a.fftSize) {
      fbuf.t = new Float32Array(a.fftSize);
      fbuf.f = new Float32Array(a.frequencyBinCount);
    }
    a.getFloatTimeDomainData(fbuf.t);
    let sum = 0;
    for (let i = 0; i < fbuf.t.length; i++) sum += fbuf.t[i] * fbuf.t[i];
    const rms = Math.sqrt(sum / fbuf.t.length);
    a.getFloatFrequencyData(fbuf.f);
    let peak = 2;
    for (let i = 3; i < fbuf.f.length; i++) if (fbuf.f[i] > fbuf.f[peak]) peak = i;
    const binHz = ctx.sampleRate / a.fftSize;
    return { rms, rmsDb: rms > 0 ? Math.max(-200, 20 * Math.log10(rms)) : -200, peakBin: peak, peakHz: peak * binHz, binHz };
  }

  window.__wctrAudio = {
    state() { return ctx ? ctx.state : "none"; },
    contextRate() { return ctx ? ctx.sampleRate : 0; },
    latency() { return ctx ? { base: ctx.baseLatency, output: ctx.outputLatency } : null; },
    pageMuted() { return globalMuted; },
    containers() {
      return Array.from(rows.values()).map((r) => ({ container: r.name, volume: r.volume, muted: r.muted, gain: r.gain.gain.value }));
    },
    pageGain() { return pageGain ? pageGain.gain.value : -1; },
    // The level after the container and page gains: what reaches the speakers.
    out() {
      if (!outTap) return { rms: 0, rmsDb: -200 };
      const b = new Float32Array(outTap.fftSize);
      outTap.getFloatTimeDomainData(b);
      let sum = 0;
      for (let i = 0; i < b.length; i++) sum += b[i] * b[i];
      const rms = Math.sqrt(sum / b.length);
      return { rms, rmsDb: rms > 0 ? Math.max(-200, 20 * Math.log10(rms)) : -200 };
    },
    stats() {
      return Array.from(streams.values()).map((s) => {
        const o = {
          stream: s.id, container: s.container, context: ctx ? ctx.state : "none", attached: !!s.node,
          sampleRate: s.rate, channels: s.ch, capacityFrames: s.cap,
          played: Atomics.load(s.hdr, 3) >>> 0, underruns: Atomics.load(s.hdr, 2) >>> 0,
          written: Atomics.load(s.hdr, 0) >>> 0,
          heartbeat: Atomics.load(s.hdr, 6) >>> 0,
          buffered: (Atomics.load(s.hdr, 0) - Atomics.load(s.hdr, 1)) >>> 0,
        };
        return s.analyser ? Object.assign(o, analyse(s)) : o;
      });
    },
  };

  return { onEngine, state, setGlobalMute, setContainer };
})();
