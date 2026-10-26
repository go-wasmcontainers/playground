# References

What the playground relies on, and where each fact was read. The engine's
own references (OCI, distribution, Docker Engine API, WASI preview 1, the
threads proposal's shared memory, wazero) are in
[engine/docs/REFERENCES.md](https://github.com/go-wasmcontainers/engine/blob/main/docs/REFERENCES.md).

## Cross-origin isolation

- MDN, `SharedArrayBuffer`, "Security requirements": shared memory needs a
  secure and cross-origin isolated context (`crossOriginIsolated`); without
  it the constructor is hidden and `postMessage` throws for a
  `SharedArrayBuffer`. The browser executor needs both (its control block,
  and the guest's shared memory).
- coi-serviceworker (`github.com/gzuidhof/coi-serviceworker`, MIT): a
  service worker adding COOP/COEP when the host cannot send headers; it must
  be its own file, served from the page's origin, over HTTPS or localhost,
  and the page reloads once on first visit. `web/sw.js` follows that
  approach, reduced to same-origin responses and `require-corp`, and also
  sets the page's CSP on every response (a worker takes its CSP from its own
  response, not from the page's meta tag).

## Blocking a worker

- MDN, `Atomics.wait`: only on an `Int32Array`/`BigInt64Array` over a
  `SharedArrayBuffer`, never on the main thread; returns `ok`, `not-equal`
  or `timed-out`. Each container's worker blocks there while the engine
  worker serves its WASI call, and sleeps there for `poll_oneoff`.

## WebAssembly JS API

- MDN, `WebAssembly.Module`: a compiled module "can be efficiently shared
  with Workers" and instantiated several times; the engine compiles once per
  module digest and memory limit and posts the module to each container
  worker.
- V8 blog, "Code caching for WebAssembly developers" (`v8.dev/blog/wasm-code-caching`):
  Chrome caches compiled code only for the streaming APIs
  (`compileStreaming`/`instantiateStreaming`) on a URL, for modules of
  128 kB or more. `engine.wasm` is loaded that way and benefits; container
  modules come from image layers (bytes, not a URL) and do not, so they are
  compiled once per page load. Storing a compiled `WebAssembly.Module` in
  IndexedDB is measured by the e2e run (`moduleInIndexedDB` in its report)
  rather than assumed.
- The WebAssembly threads proposal (`WebAssembly/threads`, Overview.md): a
  shared memory needs a maximum, and its `buffer` is a new
  `SharedArrayBuffer` after a grow.

## Go on the web

- `$(go env GOROOT)/lib/wasm/wasm_exec.js` (go1.27): the glue for a
  `GOOS=js` program (`new Go()`, `go.importObject`, `go.run(instance)`); the
  site copies it from the toolchain that built `engine.wasm`, since the two
  must match.
- `syscall/js`: `FuncOf` callbacks run on the JavaScript event loop and must
  not block; the engine answers WASI calls inside such a callback without
  blocking, and runs every command in a goroutine.

## xterm.js 5.5.0

- `xtermjs.org/docs/api/terminal/classes/terminal/`: `new Terminal(options)`,
  `open(element)`, `write(data)`, `onData` (what the user types or pastes),
  `loadAddon`. The files are fetched from the npm registry and checked
  against the registry's sha512 integrity (`cmd/site thirdparty`), then
  served same-origin.

## GitHub Pages

- `docs.github.com/en/pages/getting-started-with-github-pages/github-pages-limits`:
  a published site is at most 1 GB, bandwidth is softly limited to 100 GB a
  month, and the 10-builds-an-hour limit does not apply to sites published
  by a custom Actions workflow (this one). The page states no per-file limit
  and says nothing of headers or caching.
- Measured (`curl -sI https://go-wasmcontainers.github.io/`, 2026-10-26):
  `cache-control: max-age=600`, and no COOP/COEP headers — hence the service
  worker, and `?v=<build>` on every asset URL so a cached page still loads
  matching files; `cmd/site verify` waits until every served file hashes
  like the build.
- A project site is served at `https://<owner>.github.io/<repo>/`, beside the
  organisation site and the other project sites (the docs at `/docs/`).
  The largest file here is `engine.wasm` (about 15 MB).
