// Explore a published port with the go-fileshare explorer: a WebDAV client
// for a WebDAV server in a container (the fileshare image, say). The
// explorer runs in a frame of this page (explorer.html), in its bridge mode
// (go-fileshare/explorer v0.4.0): it finds fileshareExplorerBridge here and
// sends its requests to it. They go to the engine as port-req messages on a
// session of their own (internal/portrelay), not through the view tab: the
// explorer is this site's code, not the container's, so it needs no origin of
// its own -- and it draws on a canvas, so nothing a container answers becomes
// HTML here.
//
// The user and password are asked for once, kept in this page's memory only,
// and sent with each request as HTTP Basic; they go when the explorer closes.
"use strict";
window.wctrExplore = (() => {
  const MAX = 16 << 20; // a request, or an answer to the explorer: its piece or a listing
  const DOWNLOAD_MAX = 256 << 20; // a download: the route's bound on a response (portroute.MaxResponse)
  const TIMEOUT = 60000;
  const state = { open: false, port: 0, explorer: null, calls: [], error: "" };
  window.__wctrExplore = state; // for tests
  const pending = new Map(); // request id -> what is coming back
  let s = null; // the session: {port, auth, sid, uc, ready}
  const $ = (id) => document.getElementById(id);
  const engine = () => window.wctrPorts.engine();

  function randomID() {
    const b = new Uint8Array(18);
    crypto.getRandomValues(b);
    return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_");
  }

  // ask shows the credentials form for port; start runs when it is sent.
  function ask(port) {
    const d = $("exploredlg");
    if (!d || !engine()) return;
    $("exploreport").textContent = "The WebDAV server published on port " + port + ".";
    $("exploreuser").value = "";
    $("explorepass").value = "";
    d.dataset.port = String(port);
    d.showModal();
    $("exploreuser").focus();
  }

  function start(port, user, pass) {
    close();
    const auth = "Basic " + btoa(unescape(encodeURIComponent(user + ":" + pass)));
    let res, rej;
    const ready = new Promise((a, b) => { res = a; rej = b; });
    s = { port, auth, sid: null, uc: null, ready, res, rej };
    ready.catch((err) => { state.error = err.message; });
    engine().postMessage({ t: "port-open", rid: "x" + randomID(), port, usercontent: "" });
    state.open = true;
    state.port = port;
    state.explorer = null;
    state.calls = [];
    $("explorertitle").textContent = "Explorer: port " + port;
    $("explorerbox").hidden = false;
    $("explorerframe").src = "explorer.html";
  }

  // close unloads the explorer (its WebAssembly memory goes with the frame),
  // ends the session, and forgets the password.
  function close() {
    if (s && s.sid && engine()) engine().postMessage({ t: "port-close", sid: s.sid });
    for (const p of pending.values()) p.reject(new Error("the explorer closed"));
    pending.clear();
    s = null;
    state.open = false;
    const f = $("explorerframe");
    if (f) f.src = "about:blank";
    const b = $("explorerbox");
    if (b) b.hidden = true;
  }

  function owns(m) {
    return !!s && ((m.t === "port-opened" && String(m.rid).startsWith("x")) || (m.sid && m.sid === s.sid));
  }

  function onEngine(m) {
    if (m.t === "port-opened") {
      if (!s) return;
      if (m.error) { s.rej(new Error(m.error)); return; }
      s.sid = m.sid;
      s.uc = m.usercontent;
      s.res();
      return;
    }
    const p = pending.get(m.id);
    if (!p) return;
    switch (m.t) {
      case "port-head":
        p.status = m.status;
        p.headers = Array.isArray(m.headers) ? m.headers : [];
        break;
      case "port-chunk":
        if (!(m.data instanceof ArrayBuffer)) break;
        p.size += m.data.byteLength;
        if (p.size > p.max) {
          finish(m.id, null, new Error("the answer is larger than the explorer takes"));
          engine().postMessage({ t: "port-abort", sid: s.sid, id: m.id });
          return;
        }
        p.parts.push(new Uint8Array(m.data));
        break;
      case "port-end": {
        const body = new Uint8Array(p.size);
        let off = 0;
        for (const part of p.parts) { body.set(part, off); off += part.length; }
        finish(m.id, { status: p.status, headers: p.headers, body });
        break;
      }
      case "port-error":
        finish(m.id, { status: m.status || 502, headers: [], body: new TextEncoder().encode(m.message || "") });
        break;
    }
  }

  function finish(id, answer, err) {
    const p = pending.get(id);
    if (!p) return;
    pending.delete(id);
    clearTimeout(p.timer);
    if (err) p.reject(err); else p.resolve(answer);
  }

  // request is the bridge's: one WebDAV request of the explorer, with the
  // session's credentials, to the container's port.
  function request(method, path, headers, body) {
    return send(method, path, headers, body, MAX);
  }

  async function send(method, path, headers, body, max) {
    if (!s) throw new Error("the explorer is closed");
    if (typeof path !== "string" || !path.startsWith("/")) throw new Error("not a path: " + path);
    await s.ready;
    const id = randomID();
    const hs = (Array.isArray(headers) ? headers : []).filter((kv) => String(kv[0]).toLowerCase() !== "authorization");
    hs.push(["Authorization", s.auth]);
    // The frame's Uint8Array is of its own realm: copied into one of ours.
    const b = body ? new Uint8Array(body).slice().buffer : null;
    if (b && b.byteLength > MAX) throw new Error("the request is larger than the relay takes");
    const answer = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        finish(id, null, new Error("no answer from the container"));
        engine().postMessage({ t: "port-abort", sid: s.sid, id });
      }, TIMEOUT);
      pending.set(id, { resolve, reject, timer, max, parts: [], size: 0, status: 0, headers: [] });
      engine().postMessage({ t: "port-req", sid: s.sid, uc: s.uc, id, port: s.port, method, path, headers: hs, body: b }, b ? [b] : []);
    });
    state.calls.push({ method, path, status: answer.status });
    if (state.calls.length > 50) state.calls.shift();
    return answer;
  }

  // download saves a file the explorer asked for: read whole (the relay
  // bounds it), then handed to the browser under a sanitised name.
  async function download(path, name) {
    try {
      const r = await send("GET", path, [], null, DOWNLOAD_MAX);
      if (r.status !== 200) throw new Error("the server answered " + r.status);
      const a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob([r.body], { type: "application/octet-stream" }));
      a.download = String(name).replace(/[\u0000-\u001f\u007f<>:"/\\|?*]/g, "_").replace(/^[.\s]+/, "").slice(0, 200) || "file";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 60000);
    } catch (err) {
      state.error = "download: " + err.message;
    }
  }

  window.fileshareExplorerBridge = {
    get label() { return "port " + (s ? s.port : ""); },
    request,
    download,
    close,
    state(json) {
      try { state.explorer = JSON.parse(json); } catch (_) { /* ignored */ }
    },
  };

  document.addEventListener("DOMContentLoaded", () => {
    const d = $("exploredlg");
    if (!d) return;
    $("exploreform").addEventListener("submit", () => {
      const user = $("exploreuser").value, pass = $("explorepass").value;
      $("explorepass").value = "";
      start(Number(d.dataset.port), user, pass);
    });
    $("explorecancel").addEventListener("click", () => d.close());
    $("explorerclose").addEventListener("click", close);
  });

  return { state, ask, start, close, owns, onEngine };
})();
