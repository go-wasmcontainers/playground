// The engine's worker: Go's wasm_exec.js glue, a small IndexedDB helper
// the engine's image store calls, then the engine itself (engine.wasm,
// GOOS=js). The build id arrives as ?v=… and is kept on every asset.
"use strict";

const v = self.location.search;
importScripts("wasm_exec.js" + v);

self.idb = {
  open() {
    return new Promise((resolve, reject) => {
      const r = indexedDB.open("wctr-playground", 1);
      r.onupgradeneeded = () => {
        r.result.createObjectStore("blobs");
        r.result.createObjectStore("meta");
      };
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    });
  },
  op(db, store, mode, f) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction(store, mode);
      const req = f(tx.objectStore(store));
      tx.oncomplete = () => resolve(req.result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
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
