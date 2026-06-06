---
Status: accepted
---

# Server-side per-Session Docker Compose Stacks, behind a Runner interface

A Demo must run real full-stack apps (.NET, Go, Python, real databases), so browser-only execution (WebContainers / Node-in-browser) is rejected outright — it can't run those runtimes, which is the entire product. When a Guest plays a Project, the Runner boots that Project's services as a Docker Compose **Stack** on a server-side host, fronted by a reverse proxy; the Stack is fresh per Session and never shared between Guests. All provisioning and teardown goes through a narrow **Runner** interface — `provision(Project, SessionId) → reachable URL` / `teardown(SessionId)` — so the execution mechanism can change without touching callers, routing, or the run contract.

## Considered options

- **Browser execution (WebContainers, StackBlitz-style)** — rejected: runs only Node/WASM in-browser; can't run .NET/Go/Postgres, which is the differentiator.
- **One shared running instance per Project** — rejected: Guests would share mutable state (they poke the DB); isolation and teardown only make sense per Session.

## Consequences

- The Runner interface is the seam the isolation mechanism (ADR-0002) plugs into; keeping it narrow is what makes the isolation upgrade path cheap.
- N concurrent plays = N live Stacks, bounded by a concurrency cap (session lifecycle, TBD) — not by sharing.
