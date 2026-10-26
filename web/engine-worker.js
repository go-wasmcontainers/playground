// The engine's worker: Go's wasm_exec.js glue, a small IndexedDB helper
// the engine's image store calls, then the engine itself (engine.wasm,
// GOOS=js). The build id arrives as ?v=… and is kept on every asset.
"use strict";

const v = "?" + self.location.search.slice(1).split("&")[0]; // ?v=<build> only
importScripts("wasm_exec.js" + v);

self.idb = {
  // Version 2 adds "state": the engine's containers, logs and writable
  // layers (engine package state), next to the image blobs.
  open() {
    return new Promise((resolve, reject) => {
      const r = indexedDB.open("wctr-playground", 2);
      r.onupgradeneeded = (e) => {
        const db = r.result;
        if (e.oldVersion < 1) {
          db.createObjectStore("blobs");
          db.createObjectStore("meta");
        }
        if (e.oldVersion < 2) {
          db.createObjectStore("state");
        }
      };
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
      r.onblocked = () => reject(new Error("the database is held open by an older tab of this page"));
    });
  },
  // A write either commits whole or rejects (a transaction is atomic):
  // QuotaExceededError arrives here as the transaction's error.
  op(db, store, mode, f) {
    return new Promise((resolve, reject) => {
      let tx;
      try {
        tx = db.transaction(store, mode);
      } catch (err) {
        reject(err);
        return;
      }
      const req = f(tx.objectStore(store));
      tx.oncomplete = () => resolve(req.result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error || new Error("transaction aborted"));
    });
  },
  // Storage as the browser sees it, for `wctr system df`.
  async usage() {
    const s = navigator.storage;
    if (!s || !s.estimate) return { usage: -1, quota: -1, persisted: false };
    const e = await s.estimate();
    const persisted = s.persisted ? await s.persisted() : false;
    return { usage: e.usage ?? -1, quota: e.quota ?? -1, persisted };
  },
  get(db, store, key) {
    return this.op(db, store, "readonly", (s) => s.get(key));
  },
  put(db, store, key, value) {
    return this.op(db, store, "readwrite", (s) => s.put(value, key));
  },
  del(db, store, key) {
    return this.op(db, store, "readwrite", (s) => s.delete(key));
  },
  keys(db, store) {
    return this.op(db, store, "readonly", (s) => s.getAllKeys());
  },
};

const go = new Go();
const t0 = performance.now();
WebAssembly.instantiateStreaming(fetch("engine.wasm" + v), go.importObject)
  .then((r) => {
    self.engineStartMs = performance.now() - t0;
    return go.run(r.instance);
  })
  .catch((err) => postMessage({ t: "fatal", message: "engine: " + String(err) }));
