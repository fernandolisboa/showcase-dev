---
Status: accepted
---

# Session lifecycle: ephemeral, non-resumable, globally capped, fail-graceful

A Session is a single ephemeral play of a Project. It holds no durable state and is **non-resumable**: when it ends it is destroyed, and returning means a brand-new, re-seeded Session. This matches anonymous Guests (no account to bind a Session to) and "fresh Stack per play" (ADR-0001), and keeps teardown trivial. Saved Guest state / resumable Sessions are explicitly out of scope — they would require durable per-Session volumes plus Guest identity.

## Lifecycle

- **End conditions** (each destroys the Session): **idle timeout** (~10–15 min no activity — the primary cost lever), **max runtime** (hard absolute cap ~30–60 min, so a forgotten tab can't run forever), or **failure** (below). Numbers are tunable defaults — observe and adjust.
- **Concurrency:** a single **global cap** on concurrent Sessions, sized to host capacity ÷ per-Session budget (ADR-0002). At the ceiling, **reject** with an "at capacity" page — no queue at MVP. A per-Guest cap is deferred: the global cap already protects the host, and an anonymous-cookie guard is soft and best tuned against observed usage.
- **Failure** fails gracefully — the platform never babysits a broken demo:
  - *Won't start* (build failure, or no healthcheck within the boot timeout) → "this demo failed to start" page, clean teardown, failure/logs surfaced to the Owner.
  - *Crash mid-Session* → the container restart policy absorbs a transient blip; if it doesn't return healthy within a short grace window, tear down with a "demo crashed" message.

## Consequences

- Pre-answers shareable links: a shared link starts a *new* Session, never resumes a dead one.
- "Return to your live Session instead of spawning a duplicate" is a deferred UX refinement (applies only while a Session is still alive) — not resumability.
