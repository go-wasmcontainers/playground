# playground

[![Docs](https://img.shields.io/badge/docs-hugo%20%2B%20hextra-0079A8)](https://go-wasmcontainers.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![CI](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml/badge.svg)](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml)
[![Pages](https://github.com/go-wasmcontainers/playground/actions/workflows/pages.yml/badge.svg)](https://github.com/go-wasmcontainers/playground/actions/workflows/pages.yml)
![status](https://img.shields.io/badge/status-M1-green)

**An in-browser container playground: a terminal running `wctr` against WASI
containers**, for cloud and container training labs. A learner types
container commands in a web page — no virtual machine, no server: the
[engine](https://github.com/go-wasmcontainers/engine) runs in the page.

**Open it: <https://go-wasmcontainers.github.io/playground/>**

> **Status: M1.** `wctr pull`, `run` (`--rm`, `-d`, `--name`, `-e`, `-w`,
> `-m`, `--entrypoint`), `ps`, `logs`, `rm`, `rmi`, `images`, `inspect`,
> `kill`, with the same semantics and exit codes as the native engine,
> tested by typing them into the page in Chromium and Firefox. No shell
> (one `wctr` command per line), no networking, no stdin yet.

## Try

```console
$ wctr run --rm hello
Unable to find image 'hello' locally
…
Hello from a WebAssembly container!
$ wctr run --rm -e NAME=you greet
Hello, you!
$ wctr run -d --name counter lab count 5
$ wctr logs -f counter
$ wctr run --name failing lab exit 42
bye
[exit status 42]
$ wctr ps -a
$ wctr run --rm shfmt --version
$ wctr images
```

`help` lists the commands and the images; `clear` clears the screen; Ctrl-C
stops waiting for a command.

**Line editing, as in bash.** Tab completes commands, flags, flag values,
images (local ones for `run`, the catalogue for `pull`), container names and
IDs, and `lab`'s commands; a unique match gets a trailing space, several
complete to their common prefix, and Tab Tab lists them in columns with
their descriptions (asking first beyond 100). Completion works mid-line and
inside quotes. Up/Down and Ctrl-R search the history, kept for the tab's
session; Left/Right, Home/End, Ctrl-A/E/B/F, Ctrl-U/K/W, Ctrl-D and Ctrl-L
edit. Measured
in Chromium: a Tab is on screen **7–14 ms** after the key press.

Images: `hello`, `greet` (a CNCF wasm OCI artifact), `lab` (a toolbox:
`echo env pwd ls cat write count sleep spin hog bench fs exit esc`), and
three pkgx `wasip1` bottles — `age` 1.3.2, `go-md2man` 2.0.7, `shfmt`
3.14.1 — taken from `ghcr.io/go-pkgx/packages` at build time.

## How it works

```
page (main thread)          engine worker (GOOS=js)                 container workers
xterm.js (keys, raw) ─────► console: engine's cli/lineedit + session
                             session: wctr command tree (in-process)
   ▲  filtered output        └─► API (in-memory transport) ─► daemon
   └──────────────────────────  image store (IndexedDB) ◄─ /v2 mirror (same origin)
                                 browser executor ── one Worker per container ──► guest.wasm
                                 package wasi ◄── SharedArrayBuffer + Atomics.wait ── WASI imports
```

- **One engine, in a worker.** The engine's daemon, its API (a subset
  compatible with the Docker Engine API) and the `wctr` command tree run in
  one dedicated worker built `GOOS=js`; `wctr` reaches the API through an
  in-memory transport (a browser has no unix sockets). The page only
  forwards keys and prints: editing, history and completion are the
  engine's pure-Go line editor (`cli/lineedit`), which completes through the
  same cobra `__complete` path as `wctr completion bash`, in-process, and is
  tested natively.
- **One worker per container.** The guest is compiled once per image
  (`WebAssembly.compile`, cached by digest and memory limit) and posted to a
  fresh worker. Its `wasi_snapshot_preview1` imports block in
  `Atomics.wait` while the engine's worker serves them with the engine's
  own WASI host — the same Go code the native engine runs in its tests — so
  there is **one** file system per container, the copy-on-write tree the
  engine already has, and volumes later need nothing new. Measured: a WASI
  call (`stat`) round-trips in **~38 µs**.
- **Limits as natively.** The guest's memory is capped in the binary (the
  engine rewrites the module's memory limits, which also makes it shared so
  the engine can read it); the wall-clock limit terminates the worker (exit
  137); logs, written bytes and the number of containers are bounded.
  The preopened `/` is the container's root and `PWD` its working
  directory, which Go's `wasip1` port needs to resolve relative paths.
- **Images, same origin.** ghcr.io sends no CORS headers and the page's CSP
  allows its own origin only, so CI publishes a static, read-only `/v2`
  mirror with the site (`/playground/v2/library/<name>/…`); `wctr pull
  hello` resolves `docker.io/library/hello` to it. A pkgx bottle becomes an
  image without being repacked: its tar+gzip layer as is, plus a config
  whose `Entrypoint` is the bottle's `bin/<name>.wasm`. Pulled blobs are kept
  in IndexedDB, verified again when used, so a reload keeps the images.
- **Cross-origin isolation on GitHub Pages.** Shared memory needs COOP/COEP,
  which Pages cannot send; `sw.js` (after coi-serviceworker) re-serves the
  site's own responses with them and with the CSP, at the cost of one reload
  on the first visit.

## Security

- The page and its workers fetch their own origin only: CSP
  `default-src 'self'`, `connect-src 'self'`, `script-src 'self'
  'wasm-unsafe-eval'` (WebAssembly, no `eval`), no inline script; xterm.js
  is served from the site (fetched from npm, checked against the registry's
  sha512).
- Container output is hostile: before it reaches xterm.js the engine keeps
  text, `\t \n \r \b`, colours (SGR) and erase-in-line, and drops every other
  escape (OSC 52 clipboard writes, OSC 8 links, title and report sequences
  that echo back as input, DCS/APC/PM/SOS), C1 controls and invalid UTF-8;
  output is rate-limited (256 KiB/s, 1 MiB burst). The filter is fuzzed, and
  the browser test prints an OSC 52 and a `javascript:` link from a
  container and checks neither reaches the terminal.
- Each container is a worker with no DOM; its only door to the engine is the
  WASI host, which bounds every argument (fuzzed in the engine).

## Performance

The same `hello` module (2.4 MB, Go `wasip1`), on one GitHub-hosted
`ubuntu-latest` runner in one CI run (`e2e` and `perf` jobs); browser
numbers are from Enter to the prompt, including compile:

| | Chromium (headless) | Firefox (headless) | Native: engine on wazero | Native: wasmtime 50 |
|---|---|---|---|---|
| engine ready (page load to prompt) | 0.46 s | 1.0 s | — | — |
| first `run` (compile + run) | 81 ms | 291 ms | 825 ms | 23 ms (`wasmtime run`, a process) |
| next `run` (compiled module cached) | 47 ms | 231 ms | 5.4 ms | — |
| WASI round trip (`lab bench`: one `stat`) | 97 µs | 354 µs | — | — |

On an Apple M4 Max (Chromium, local): ready 0.25 s, first run 31 ms, next
15 ms, WASI round trip 38 µs; the native engine 430 ms / 2.3 ms.

The browsers compile the guest lazily (a baseline tier first), wazero
compiles it ahead of time, which is why the engine's native cold start is
the slowest here and wasmtime's the fastest; a warm start in a browser pays
a worker's creation and instantiation, more so in Firefox. A compiled
`WebAssembly.Module` cannot be stored in IndexedDB (measured:
`DataCloneError` in both browsers), so container modules compile once per
page load.

## Building and testing

```console
$ go run ./cmd/site build --out _site --pkgx          # images + mirror, engine.wasm, the page
$ go run ./cmd/site serve --dir _site                  # http://127.0.0.1:8080/playground/
$ go test ./...                                        # native: session transcript, filter, mirror
$ go run ./cmd/site playwright-driver --dir /tmp/pwd   # once: driver from npm playwright-core
$ PLAYWRIGHT_DRIVER_PATH=/tmp/pwd PLAYWRIGHT_NODEJS_PATH=$(command -v node) \
  go run github.com/playwright-community/playwright-go/cmd/playwright install chromium firefox
$ PLAYWRIGHT_DRIVER_PATH=/tmp/pwd PLAYWRIGHT_NODEJS_PATH=$(command -v node) go test -tags e2e ./e2e/
```

The `pages` workflow publishes the site, then `cmd/site verify` waits until
every file served at the live URL hashes like the build (Pages caches for
600 s), then the same e2e session is typed into the live page in Chromium
(`E2E_URL=https://go-wasmcontainers.github.io/playground/ go test -tags e2e -run TestLive ./e2e/`).

## Limitations

- One `wctr` command per line, no shell, no pipes; containers have no
  stdin and no network.
- Containers do not survive a reload (images do).
- Interrupting `wctr run` with Ctrl-C stops waiting; the container keeps
  running (`wctr kill`).

## Links

- [Website](https://go-wasmcontainers.github.io) and
  [documentation](https://go-wasmcontainers.github.io/docs/)
- [engine](https://github.com/go-wasmcontainers/engine): the container engine
- [docs/REFERENCES.md](docs/REFERENCES.md): what each design choice rests on

## Licence

BSD-3-Clause. `web/vendor/` holds xterm.js and its fit addon (MIT, licences
alongside).
