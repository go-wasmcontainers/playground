# playground

[![Docs](https://img.shields.io/badge/docs-hugo%20%2B%20hextra-0079A8)](https://go-wasmcontainers.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![CI](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml/badge.svg)](https://github.com/go-wasmcontainers/playground/actions/workflows/ci.yml)
![status](https://img.shields.io/badge/status-planned-lightgrey)

**An in-browser container playground: a terminal running `wctr` against WASI
containers**, for cloud and container training labs. A learner types container
commands in a web page, with no virtual machine to provision.

> **Status: planned. Nothing is written yet.** This repository is the home of
> the playground, reserved before its code. It needs the
> [engine](https://github.com/go-wasmcontainers/engine)'s executor to run in a
> browser worker, and the engine's first milestone (M1, native) is still in
> progress. There is no Go Reference badge because there is no Go module yet;
> the CI badge is the README's link check, the only thing to check so far.

## What it will be

- a terminal in the page, running the `wctr` commands the engine serves
  (`pull`, `run`, `ps`, `logs`, `rm`, `rmi`, `inspect`, `images` first);
- WASI (`wasip1/wasm`) containers executed in browser workers, so the lab is a
  web page;
- labs for cloud and container training on top of that.

Networking, a shell and `exec`, building from a Dockerfile, Compose and a
mini-Kubernetes are all **planned** for the engine and are not promised here
before they exist.

## Links

- [go-wasmcontainers](https://github.com/go-wasmcontainers): the organisation
- [Website](https://go-wasmcontainers.github.io) and
  [documentation](https://go-wasmcontainers.github.io/docs/): architecture and
  roadmap
- [engine](https://github.com/go-wasmcontainers/engine): the container engine

## Licence

BSD-3-Clause.
