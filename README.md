# showcase-dev

A platform for per-user developer **Portfolios** whose differentiator is
**live-running Projects**: a Guest clicks "play" on a Project, a real full-stack
app boots in an isolated **Session** in the browser, and tears down on idle.

- **What & why:** [`CLAUDE.md`](./CLAUDE.md), [`SHOWCASE_PLAN_SEED.md`](./SHOWCASE_PLAN_SEED.md)
- **Language / glossary:** [`CONTEXT.md`](./CONTEXT.md)
- **Decisions:** [`docs/adr/`](./docs/adr) — the stack is recorded in
  [ADR-0009](./docs/adr/0009-implementation-stack-go-traefik-single-vm.md).

## Stack (ADR-0009)

Go control plane + Runner · React (Vite/TS) UI embedded into the Go binary via
`go:embed` · Postgres · orchestration by shelling out to `docker compose` behind
the Runner interface (ADR-0001) · Traefik reverse proxy · a single Azure VM
running Docker + gVisor.

## Layout

```
cmd/controlplane/      # entrypoint: serves the embedded UI + control-plane API
internal/
  config/              # env-based configuration
  server/              # HTTP routes: /healthz, /api (later), SPA fallback
  web/                 # go:embed of the built React app + SPA handler
  runner/              # ADR-0001 Runner interface (Session lifecycle) — stub until #8
web/                   # React + Vite + TypeScript app (builds into internal/web/dist)
docs/adr/              # architecture decision records
.github/workflows/     # CI: Go (vet/test/build), Web (lint/test/build), full binary
```

Bounded contexts (Runner / Portfolio / Import per `CONTEXT-MAP.md`) get carved
into their own packages + `CONTEXT.md` as the architecture firms up.

## Prerequisites

- **Go 1.26+**, **Node 24+** (npm 11+)
- **Docker** (optional locally — only for the dev Postgres in `compose.dev.yml`)

## Quickstart

```bash
make build      # build the React UI and embed it into the control-plane binary
make run        # build, then run — http://localhost:8080  (health: /healthz)
make test       # Go tests + web tests
make lint       # go vet (+ golangci-lint if installed) + eslint
make help       # list all targets
```

Faster UI loop (hot reload), two terminals:

```bash
make dev        # terminal 1: Go control plane on :8080 (serves API + /healthz)
make web-dev    # terminal 2: Vite dev server, proxies /api + /healthz to :8080
```

Local database (needs Docker): `make db-up` / `make db-down`. Copy `.env.example`
to `.env` to set `PORT`, `DATABASE_URL`, etc.

## Live-Session proxy (local)

Each booted Session is reachable at its own subdomain `s-<id>.run.<demo-domain>`,
behind Traefik (ADR-0004/0009). The control plane owns the routing map and serves
it to Traefik over the HTTP provider; a Guest sees a booting page until the Stack
is healthy, then the live Demo, with same-origin `/api`.

```bash
INTERNAL_PORT=8081 make run   # control plane: app on :8080, Traefik surface on :8081
make proxy-up                 # Traefik in front of Sessions (deploy/traefik/)
# A booted Session (Runner wired with WithProxy(registry, "showcase-traefik")) is
# then reachable at  http://s-<id>.run.localhost   (*.localhost -> 127.0.0.1)
make proxy-down
```

The Traefik-facing endpoints (`/traefik` dynamic config + the booting splash) live
on a **separate internal port** (`INTERNAL_PORT`), never the public app port — a
Demo must never reach the control plane. **Production** swaps the dev config for a
`websecure` entrypoint with an ACME **DNS-01 wildcard cert** for
`*.run.<demo-domain>` on **Cloudflare** (Traefik ships the provider in-binary;
config-only) — see `deploy/traefik/traefik.yml`.

## CI

GitHub Actions runs on every PR and on push to `main`: a **Go** job
(`vet` + race `test` + `build`), a **Web** job (`lint` + `test` + `build`), and a
**binary** job that builds the UI and embeds it into the Go binary end-to-end.
Seam-2 integration tests (`-tags integration`, Docker) run in a dedicated job.
