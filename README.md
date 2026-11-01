# playground

[![Docs](https://img.shields.io/badge/docs-hugo%20%2B%20hextra-0079A8)](https://go-wasmcontainers.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![CI](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml/badge.svg)](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml)
[![Pages](https://github.com/go-wasmcontainers/playground/actions/workflows/pages.yml/badge.svg)](https://github.com/go-wasmcontainers/playground/actions/workflows/pages.yml)
![status](https://img.shields.io/badge/status-M1-green)

**An in-browser container playground: a dashboard of WASI containers,
images and volumes, with a terminal running `wctr`**, for cloud and
container training labs. A learner types container commands in a web page
and sees what they do — no virtual machine, no server: the
[engine](https://github.com/go-wasmcontainers/engine) runs in the page.

**Open it: <https://go-wasmcontainers.github.io/playground/>** — the
[dashboard](https://github.com/go-wasmcontainers/dashboard) (Containers,
Images, Volumes, Terminal; logs and stats of the selected container, stop,
start and remove buttons that print the `wctr` command they ran). The
terminal alone, on xterm.js, stays at
[`?ui=terminal`](https://go-wasmcontainers.github.io/playground/?ui=terminal).

> **Status: M1.** `wctr pull`, `run` (`--rm`, `-d`, `--name`, `-e`, `-w`,
> `-m`, `--entrypoint`), `ps`, `logs`, `start`, `diff`, `rm`, `rmi`,
> `images`, `inspect`, `kill`, `system df`, `system prune`, with the same
> semantics and exit codes as the native engine, tested by typing them into
> the page in Chromium and Firefox. Images and containers survive a reload;
> the page works offline. Containers talk to each other over TCP and UDP,
> by name ([Networking](#networking)). One `wctr` command per line; the
> `shell` image gives a shell, and a line with `exec -i` gets what you type
> (v0.4.0). No `-p` yet.

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
edit. Measured from the key press to the completed line on screen (sampled
every 5 ms): **7–14 ms** in Chromium, **23 ms** in Firefox (CI runner).

**It keeps your work.** Pulled images, containers (state, exit code, logs,
the files they wrote) live in the browser's IndexedDB: a reload, or coming
back later, finds them as you left them. A container that was running is
then exited with status 137, and a `--rm` one is gone, as after a daemon
restart (`wctr diff NAME` shows what a container wrote; `wctr start NAME`
runs it again). The page itself is kept by its service worker, so a reload
works offline; a new version installs in the background and is used only
when you click "Reload to update". One tab runs the engine: a second tab
of the same browser offers to take over. `wctr system df` shows what is
stored and the browser's quota; `wctr system prune -f` (`-a` for unused
images too) frees it. After the first `pull` or `run` the page asks the
browser to keep its storage (`navigator.storage.persist()`): Chromium and
Safari decide without asking, Firefox asks you.

Measured in Chromium (CI numbers are in each run's summary): the engine is
ready in 130–190 ms on a first visit, 80–115 ms after a reload, 75 ms
offline. Compiled modules are not kept across reloads by default: measured,
compiling a 2.8 MB module from Cache Storage with `compileStreaming` takes
8.4 ms against 1.5 ms from bytes, and the guest runs no faster for it
(`?codecache=1` turns it on).

Images: `hello`, `greet` (a CNCF wasm OCI artifact), `lab` (a toolbox:
`echo env pwd ls cat write count sleep spin hog bench fs exit esc`),
`mnist` (reads a handwritten digit with **wasi-nn**: `wctr run --rm mnist`
prints `digit 7 (p=1.000)`, in about 80 ms in Chromium; the engine worker
serves `wasi_ephemeral_nn` with the pure-Go CPU backend of
[go-wasmcontainers/gpu](https://github.com/go-wasmcontainers/gpu) v0.5.1;
`wctr run --rm --gpu mnist gpu` runs it on the page's WebGPU, and without
`--gpu` the GPU target fails with "unsupported operation", it never falls
back to the CPU. The model is MNIST-12 from the ONNX Model Zoo (MIT); the picture is
picture 0 of the MNIST test set by Y. LeCun, C. Cortes and C. J. C. Burges
(CC BY-SA 3.0). The image carries both notices at
`/usr/share/doc/mnist/NOTICE`), and these:

- `compute`: **wasi:webgpu** compute. `wctr run --rm --gpu compute` runs a
  1M-float vector add and a 256x256 matrix product on the GPU, and checks
  both against the CPU.
- `triangle`: **wasi-gfx:surface**. `wctr run --rm --gpu triangle [frames [input]]`
  draws a spinning triangle on a surface. In the dashboard the surface is a
  pane under **Surfaces**; the page acknowledges each frame, and the engine
  paces the guest on those acknowledgements. Click the pane to give it the
  focus: its pointer presses and keys go to the guest. The dashboard sends
  `{t:"input"}`, which the engine worker hands to `webgpu.SurfaceInput`. With
  `input`, the guest prints what it receives: `pointer-down 80,80`, then
  `key-down key-a "A" shift=true`.

GPU access is opt-in per container. A container without `--gpu`, or a
browser without WebGPU, gets no adapter: these images print
`no GPU adapter` and exit 2. Software (fallback) adapters are refused.

- `shell`: [go-wasmcontainers/shell](https://github.com/go-wasmcontainers/shell)
  0.5.0, mirrored verbatim from `ghcr.io` and pinned by digest
  (`go run ./cmd/site pin REF` prints the digest to pin). It contains `sh`
  (with Tab completion, and the shared line editor: Ctrl-R search, wide
  characters) and 42 coreutils. Programs started by the shell run as WASIX processes in
  the browser:

  ```console
  $ wctr run --rm -it shell
  $ wctr run -d --name c shell sleep 600
  $ wctr exec c sh -c 'ls /bin | wc -l'
  44
  $ wctr exec -it c sh
  ```

  In the terminal, a line whose `wctr` flags say `-i` (`run -i`,
  `exec -i`, `start -ai`) gets what you type, Ctrl-C and Ctrl-D included.
  Ctrl-P Ctrl-Q detaches from `run -it` and leaves the container running. `wctr build` and `wctr compose` are wired in as in
  `cmd/wctr`.

Last, **every pkgx `wasip1/wasm` bottle**, taken from `ghcr.io/go-pkgx/packages`
at build time. The list is not typed anywhere: `go run ./cmd/site build --pkgx`
reads pkgx's signed catalogue (`go-pkgx.dev/catalog`, `bottle.FetchCatalog`,
which verifies the signature), keeps the projects whose `versions` is not
empty for `wasip1/wasm`, and mirrors the newest version of each, so a bottle
published tomorrow is an image after the next build with no code change. At
the time of writing that is 43 images from 41 bottles (30 Go projects such as
`yq`, `gojq`, `jj`, `shfmt`, `actionlint`, `age`, and C ones such as `lua`,
`luac`, `sqlite3`, `zstd`, `lz4`, `brotli`, `simdjson-minify`; the C layers are
`tar+zstd`, served as published). The page's `wctr images` and the dashboard's
Images view list whatever `catalog.json` holds, and each entry names its
`project`, `version`, layer `bytes` and `checked`: `true` when the build ran
the image under the engine and wasmtime and nothing failed or differed (40 of
43 today with engine v0.30.0), `false` when it is only mirrored (the named
expected cases in `internal/mirror/testdata/smoke.json`: `tomlv`, `gnostic`).
`wctr images` and completion show `[checked]` or `[mirrored]` after the
description. Engine v0.30.1 fixed what made `yq` differ (piped stdio is no
longer reported as a tty) and reports out-of-range exit codes as 1, as `gnostic`
needed; wasmtime still prints its own trap message on stderr for `gnostic`.

The rules, each tested in `internal/mirror`:

- **Version**: the highest by numeric components, then rebuild counter
  (`1.8.2_1` &gt; `1.8.2`), then raw string; the catalogue's order is not trusted;
  a string that is not version-shaped or not an OCI tag is skipped.
- **Names**: an image per `bin/<name>.wasm` in the bottle (`lua` gives `lua`
  and `luac`), all sharing the bottle's layer, at most 8 per bottle, names in
  `provides` first. `provides` is only a hint: on wasip1/wasm it is copied from
  the host recipe and wrong both ways (go-pkgx/bk#376), so a name without a
  module, and a module it does not list, are findings. An image's entrypoint is
  the bare binary name, resolved on `PATH`, so argv[0] is `lua`, not the module
  file. A name taken by another project goes to the
  project whose last path segment equals it, else the smallest project name; the
  others become `<project-with-dashes>-<name>`. A first-party image keeps its name.
- **Dependencies are ignored**: a `wasip1` module is statically linked, and the
  catalogue's `deps` are read from the host recipe.
- **Limits** (`mirror.DefaultBottleLimits`): 400 projects, 900 images, 8 images per
  bottle, 100 MiB per compressed layer, 512 MiB and 20 000 entries unpacked, a
  3 minute catalogue timeout and 30 minutes in all. An unverifiable or empty
  catalogue, a network error and a count over its limit stop the build: there is
  no built-in list to fall back to. A bottle over a size limit, or whose tar has
  an absolute path, a `..`, or a link leaving the root, is refused and listed in
  `findings.json`, and costs only itself.
- **Page cost**: one `catalog.json` (about 330 bytes per image, read up to 1 MiB
  and 1000 entries), then an image's blobs only when a command uses it.
- **Site size**: GitHub Pages refuses a site over 1 GB. The mirror publishes the
  bottles' compressed layers, once per image (every image has its own `/v2`
  repository). Measured on 2026-10-31 with 43 images: 112 MB of `v2/`, 167 MB
  for the whole site (engine 29 MB, dashboard 25 MB), about 2.6 MB per image, so
  roughly 300 images would reach the ceiling. (The 441 MB figure is the
  modules unpacked, which is not what is served.) `Limits.MaxSiteBytes`, 700 MiB
  of layers, is a hard error well under it. If the set outgrows it: first drop the
  largest layers (`gosec` is 20 MB), then shard the mirror across a second Pages
  site (a second origin: the page's CSP allows its own origin only, so this
  needs a policy change), or serve blobs from a release asset (which needs CORS,
  which release downloads do not send).
- **Not deterministic**: a tag can serve different bytes over time
  (`simdjson` 5.0.5, go-pkgx/packages#393). The mirror keeps no per-version cache
  and the smoke compares behaviour on the bytes it fetched, never a digest.

`go run ./cmd/mirror smoke --require-wasmtime --spec internal/mirror/testdata/smoke.json`
runs every derived image under the engine (wazero) and under wasmtime with
`--version` and `--help` (or the probe the spec names, e.g. `lua -v`), and
compares exit status, stdout and stderr byte for byte; CI does it on every
change (`pkgx bottles`), with the findings for go-pkgx in the job summary. A
failure or difference that the spec does not name fails the job, and a named one
that no longer happens fails it too.

lua raises its errors with WebAssembly
exception handling, which runs in Chromium and Firefox (tested) and natively:

```console
$ wctr run --rm lua -e 'print(pcall(function() error("boom") end))'
false	(command line):1: boom
$ wctr run --rm sqlite3 :memory: 'select 6*7;'
42
```

## Networking

```console
$ wctr run -d --name web net serve
$ wctr run --rm net get http://web/me
hello me from 10.88.0.3:49153
$ wctr run -d --name echo net udp-echo
$ wctr run --rm net udp echo:7 ping
reply: ping
$ wctr run --rm net lookup nobody
net: lookup nobody on 127.0.0.1:53: no such host
$ wctr network create lab && wctr run -d --network lab --name db net serve
```

Every container joins the `bridge` network (or `--network NAME`; `wctr
network create|ls|inspect|rm`), in the page: the engine (v0.10.0) serves
the WasmEdge socket calls of a guest's WASI imports and answers its DNS
queries with the network's names, so a stock Go program using
[stealthrocket/net](https://github.com/stealthrocket/net) runs unchanged —
the `net` image is one (`serve`, `get`, `bench`, `udp-echo`, `udp`,
`lookup`, `dial`). Nothing leaves the page: there is no route to the
Internet or to the machine.

Measured (`net bench`: 200 sequential GETs, then one 8 MiB GET, between two
containers on the bridge), Apple M4 Max, load 16–19 so indicative:

| | latency p50 / p90 | throughput |
|---|---|---|
| Chromium (the page) | 1.1 / 1.4 ms | 106–112 MB/s |
| native: the same engine on wazero (`perf` job) | 0.33 / 0.39 ms | 1.2 GB/s |
| native: Go `net/http` over loopback TCP (upper bound) | 0.05 / 0.07 ms | 3.8 GB/s |

The browser is ~3× slower in latency and ~11× in throughput than the same
engine natively: every socket read or write is a WASI call that crosses
from the container's worker to the engine's (a `SharedArrayBuffer` round
trip, ~40–100 µs). Larger reads per call are the open item.

**Join class.** With a teacher's
[relay](https://github.com/go-wasmcontainers/relay), the page joins the
class network: in the dashboard's **Networks** view (Alt+4), **Join class…**
asks for the relay's address (https only; the page reloads once), then your
name and the join code (on the `?ui=terminal` page, the "Join class" link
under the header does the same). A `class` network then
appears (`wctr run --network class …`): your containers get the class's
addresses and names (`web.<you>.<domain>`), reach your classmates' under
the teacher's firewall, and are reached by them. "Leave class" ends the
session.

- The engine worker alone talks to the relay: the service worker extends
  *that worker's* CSP `connect-src` to the one relay origin you entered
  (https and wss), and the page keeps `connect-src 'self'` (both tested).
- The join code is a secret: typed in a password field, posted once to the
  engine worker, cleared; never stored, logged, put in a URL or the
  history (the e2e checks every place the page keeps things). The session
  token is kept in IndexedDB so a reload rejoins without the code, and is
  deleted by "Leave class".
- The relay's certificate must be one the browser trusts: a publicly
  trusted one (`wctr-relay serve --tls acme`), or the classroom CA
  (`wctr-relay ca export`) installed in the browser or the system. A page
  cannot pin a CA itself; Firefox keeps its own certificate store, so
  there the classroom CA goes in Firefox's settings (or an enterprise
  policy), not only the system's.
- **The teacher runs the relay for the playground's origin**:
  `wctr-relay serve --cors-origin https://go-wasmcontainers.github.io`
  (relay v0.3.0 or later; repeatable, `*` refused). That allows the join
  calls' CORS preflight and the data channel's Origin check. Without it,
  joining fails as a bare "NetworkError" (in Firefox first: it sends the
  Connect client's `User-Agent`, which the preflight must allow). The e2e
  runs a real relay configured that way.
- The page and the dashboard meet through `window.__wctrClass` (`relay`,
  `useRelay(origin)`, `status()`), the protocol of the dashboard's README
  ("Join class"); the e2e drives the dashboard's own dialog, one key at a
  time, and checks the code is in no URL, storage, DOM or test hook.

**`-p` in the browser, on an origin of its own.** After `wctr run -d -p
8080:80 net serve`, the bar above the terminal shows **open :8080**,
which opens the container's web pages in a tab of their own. They are
**not on this origin**: a container's page here could read the
playground's storage (the class session token) and script it, and a
sandboxed page cannot be served by a service worker at all (an opaque
origin has no active service worker, Service Workers §2.5.1; measured in
Chromium: its stylesheets, scripts, images and `fetch` all bypass the
worker). So containers' pages live on **another site**,
`go-wasmcontainers-usercontent.github.io` (github.io is on the Public
Suffix List):

```
playground tab (this origin, isolated: the engine)
   ▲ BroadcastChannel, per-request ids bound to a nonce the playground issued
view tab (this origin, not isolated, not frameable)
   └─ iframe <usercontent origin>/shell.html?s=<session>#<nonce>
        └─ iframe /s/<session>/8080/…  ← the container's page; its requests go to
           the usercontent service worker → shell → view → engine → portmap.Virtual
```

- The **shell** (`internal/usercontent`, written by `site usercontent
  --allow <playground origin>`; each `<org>-usercontent` Pages repository
  runs it, so one implementation serves several playgrounds) talks to the
  pinned origin only, accepts only messages carrying the session's nonce
  (read from its URL fragment, then removed from it), refuses to run
  outside that origin (`ancestorOrigins`, else the referrer; its service
  worker adds `frame-ancestors` once installed, as Pages sends no
  headers), and caches nothing.
- **What a container may say** is decided in Go (`internal/portroute`,
  fuzzed): methods, paths and header fields bounded; `Cookie`, `Host` and
  forwarding fields from the page dropped, `X-Forwarded-Host/Proto/Prefix`
  set; `Set-Cookie`, `Clear-Site-Data`, `Service-Worker-Allowed`,
  hop-by-hop and the origin's own policies never reach the browser;
  `Location` to `localhost:<port>` rewritten to that port's path, so an
  application can send the browser to an identity provider in another
  container. A service worker can neither see nor set cookies, so the
  engine keeps a cookie jar per session (sessions work; `document.cookie`
  in the container's page does not see them).
- The shell is not a boundary against the container's own pages (they
  share its origin); what they reach through it is HTTP to this session's
  published ports, nothing of the playground.

- The **engine** publishes ports in memory (engine `portmap.Virtual`) and
  issues a **session** per opened port (`internal/portrelay`). A session
  is a random id naming the port's pages (`/s/<sid>/<port>/`) and a nonce
  for the shell, bound to that one port and to the usercontent origin. A
  request for a session it did not issue, or for another port, is refused.
  Each session has its own cookie jar.
- The **view tab** (`view.html`) is this origin, not cross-origin isolated
  (it embeds the shell, of another site) and not frameable. It takes the
  nonce out of its URL at once, and relays between the shell and the
  playground tab over a `BroadcastChannel`. The playground tab answers only
  for the sessions it opened, so two playground tabs never answer for each
  other. Closing the view ends the session.
- **Measured** in Chromium (`e2e/ports_test.go`): 8 MiB from a container to
  the page, through the service worker, shell, view, playground tab, engine
  and back, in 32 ms (about 265 MB/s, M4 Max, under load).

Tested end to end: `go test -tags e2e -run TestPublishedPort ./e2e/`, with
the playground and the usercontent site on two local origins. The container's
page shows, an 8 MiB body arrives whole, the nonce leaves the URL, and the
view is not isolated. With the engine's relay disabled, the test fails.

### A file server in the tab: the `fileshare` image

```console
$ wctr run -d -p 8080:80 --name files fileshare
```

The image is [go-fileshare/fileshare](https://github.com/go-fileshare/fileshare),
built for `wasip1` and serving WebDAV only (6.5 MB gzip):
- one share, `files`, with a README in it;
- one person: user `alice`, password `playground`. The password is a demo,
  written into the image on purpose, since nothing the share holds belongs to
  anybody;
- plaintext WebDAV, because TLS ends at the browser's origin and the hop to
  the container is the engine's, in memory.

**open :8080** shows the server's page, and the route relays WebDAV's methods
(`PROPFIND`, `MKCOL`, `COPY`, `MOVE`, `LOCK`…; RFC 4918) as well as HTTP's, so
a WebDAV client in the page works. `e2e/ports_test.go` `TestFileshare` checks,
from inside the container's page:
- without credentials: 401 with a `Basic` challenge;
- with a wrong password: 401;
- with alice's: a PROPFIND of the share (207) lists README.txt, which reads;
- a PUT lands (201) and reads back.

**explore :8080** opens the [go-fileshare explorer](https://github.com/go-fileshare/explorer)
(v0.4.0, built into the site as `explorer.wasm`, fetched only when used) as a
browser client of the server:
- It asks for a user and a password first. They are sent as HTTP Basic with
  each request, and they stay in the page's memory. They are dropped when the
  explorer closes; nothing is stored.
- The explorer runs in a frame of the playground, in its bridge mode. Its
  requests go straight to the engine, on a relay session of their own, with no
  view tab and no usercontent origin.
- The explorer is the site's own code, not the container's, and it draws on a
  canvas. Nothing the container answers becomes HTML in the playground.
- `explorer.html` is the only page that may be framed, and only by the
  playground (`frame-ancestors 'self'`; everything else keeps `'none'`).
- The session's credentials replace any `Authorization` header the frame
  sends.
- A download is read whole, up to the route's 256 MiB bound, and saved under
  a sanitised name.

`TestFileshareExplorer` checks:
- a wrong password: 401, and the explorer says the server refused the login;
- alice's: the server lists its share;
- through the bridge, a PUT (201) reads back;
- a frame-supplied `Authorization` is replaced;
- after close, the bridge refuses.

## How it works

```
page (main thread)          engine worker (GOOS=js)                 container workers
dashboard.wasm (canvas) ──► API over postMessage (dashboard bridge) ─► daemon
  its Terminal: session (wctr) in the page, API over the same bridge
or ?ui=terminal:
xterm.js (keys, raw) ─────► console: engine's cli/lineedit + session
                             session: wctr command tree (in-process)
   ▲  filtered output        └─► API (in-memory transport) ─► daemon
   └──────────────────────────  image store (IndexedDB) ◄─ /v2 mirror (same origin)
                                 browser executor ── one Worker per container ──► guest.wasm
                                 package wasi ◄── SharedArrayBuffer + Atomics.wait ── WASI imports
```

- **The dashboard rides the same engine.** `dashboard.wasm` (go-widgets on
  a `<canvas>`, 20 MB) is downloaded and compiled while the engine worker
  starts, and runs once the engine is ready; it speaks the engine's HTTP API
  to the worker through the dashboard's postMessage bridge and follows
  `/events`. Its Terminal runs the playground's session (wctr, `help`,
  `clear`, the image catalogue for `pull`) in the page. The service worker
  caches it with the rest of the shell, so the dashboard works offline.
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
$ go run ./cmd/site build --out _site --pkgx --remote # images + mirror (--remote: the pinned shell), engine.wasm, the page
$ go run ./cmd/site serve --dir _site                  # http://127.0.0.1:8080/playground/
$ go test ./...                                        # native: session transcript, filter, mirror
$ go run ./cmd/site playwright-driver --dir /tmp/pwd   # once: driver from npm playwright-core
$ PLAYWRIGHT_DRIVER_PATH=/tmp/pwd PLAYWRIGHT_NODEJS_PATH=$(command -v node) \
  go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium firefox
$ PLAYWRIGHT_DRIVER_PATH=/tmp/pwd PLAYWRIGHT_NODEJS_PATH=$(command -v node) go test -tags e2e ./e2e/
```

The e2e has three opt-in parts:

- `E2E_REMOTE=1` mirrors the shell image and runs `TestShell`: exec, a
  pipeline, and `exec -it` and `run --rm -it shell` typed into the page
  (`echo`, then `exit 4`, the status the line ends with).
- `E2E_GPU=1` runs `TestGPU` in Chromium (channel `chromium`):
  - `compute` matches the CPU;
  - `triangle`'s frames reach the page and change;
  - without `--gpu`, there is no adapter.

  It also runs `TestGPUSurfaceInput`. That test clicks a known point of the triangle's pane and types Shift+A. The guest must log `pointer-down`/`pointer-up` at the surface coordinates the pane maps the point to (letterbox included), then `key-a "A" shift=true`, down and up. It fails when the worker does not forward the input.
- `E2E_GPU_REAL=1` makes a software adapter fail the test. CI sets it on
  the macOS lane, which runs on the Apple GPU. The Linux lane has only
  SwiftShader on Vulkan
  (`E2E_GPU_ARGS="--enable-features=Vulkan --use-vulkan=swiftshader --use-angle=swiftshader"`).
  The engine refuses that software adapter even with `--gpu`, and the
  Linux lane checks the refusal.

The `pages` workflow publishes the site, then `cmd/site verify` waits until
every file served at the live URL hashes like the build (Pages caches for
600 s), then the same e2e session is typed into the live page in Chromium
(`E2E_URL=https://go-wasmcontainers.github.io/playground/ go test -tags e2e -run 'TestLive|TestDashboard' ./e2e/`).
`TestDashboard` drives the dashboard as a person does — one key press per
character, clicks at the coordinates the dashboard reports for its controls
(`window.__dashboard.state()`), never pixels: a run typed in its Terminal
appears in Containers, its logs show, the Stop button stops it, a reload
restores it, and (service worker) an offline reload brings the dashboard
back. Chromium (service worker and headers) and Firefox (headers) in CI, and
Chromium against the live page: Playwright's Firefox runs no service
workers, and GitHub Pages cannot send the isolation headers, so there is no
live Firefox run.

## Limitations

- One `wctr` command per line, with no pipes in that line (the `shell` image
  has `sh`); a container's stdin is typed into with `-i` (see Images).
  Networks stay inside the page (no Internet, no published ports yet: the
  usercontent origin is deployed, `-p` is not live).
- A running container's writes are stored every 2 s: a reload loses at
  most the last 2 s of them. Storage is best effort unless the browser
  grants `persist()`: under disk pressure it may evict the whole origin.
- When a second tab takes over, the first tab's engine stops at once; a
  write it had under way is lost (each write is one IndexedDB
  transaction, so nothing is left half-written).
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
