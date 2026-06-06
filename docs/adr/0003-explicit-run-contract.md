---
Status: accepted
---

# Projects declare an explicit run contract: Owner Dockerfile(s) + a platform manifest, built from source

A Project's runnability is **explicitly declared, not inferred**. The Owner provides a **Dockerfile per service** in their repo(s) and fills a **platform manifest** describing the topology; the platform clones the repos, **builds the images from source**, and **generates** a locked-down Compose Stack from the manifest. The platform never *generates* a Dockerfile (deferred convenience — see Non-goals) and never accepts a *pre-built image* (an opaque blob with no provenance). This keeps the Owner experience to "point at a repo + declare a few fields," keeps build inputs inspectable, and lets the platform own every security-sensitive part of the compose.

## What the manifest declares

- **Services** — for each: `{ repo, dockerfile, port, role: ui | api }`. Two-repo linking (a UI repo + one or more API repos) is expressed here.
- **`db`** (optional) — `{ engine, version }`. The platform provides exactly the engine the Project declares and **never substitutes one** (it cannot force SQLite on a Postgres app — the provider abstraction lives in the app, not the platform). An app that embeds SQLite declares no `db` and ships the file in its container.
- **`env`** — each var as `{ name, source: platform | static | owner, secret? }`. `platform` values (DB creds, bind port, API URL) are minted per Session; `static` are plaintext config; `owner` are encrypted-at-rest and **test-credential-only** (a demo cannot hold a real secret — see ADR-0002's threat model). **MVP injects only `platform` + `static`;** the encrypted `owner` store is declared-but-deferred.
- **`migrate` / `seed`** (optional) — a one-shot the platform runs against the fresh DB before readiness; otherwise the app self-migrates on startup. Readiness is gated by a **healthcheck** (which the proxy needs regardless).

## Build, cache, run

- **Build at publish time**, cache the image, **invalidate on new commit** (not a time-TTL), LRU-evict cold images. Guests never wait on a build; the Owner gets build/run feedback at config time.
- Each Session boots a **fresh Stack from cached images** + a **fresh in-Stack ephemeral DB** (a service in the Stack, torn down with the Session), seeded per boot. Image storage is negligible; the real cost lever is Stack runtime (idle-teardown), not the image cache.

## The platform owns the generated compose

The Owner declares; the platform *generates* the actual Compose, baking in every invariant structurally so the Owner cannot opt out: `runtime=runsc` + resource caps (ADR-0002), per-Session bridge network, non-root, read-only rootfs, **no host networking, no bind mounts, no Docker socket, no `privileged`**, scoped env only (platform secrets never enter a demo; per-Stack generated DB creds).

## Non-goals (MVP)

- **Auto-detection / auto-generation of Dockerfiles** — deferred convenience; an explicit Dockerfile is always required and remains the escape hatch even once generation exists.
- **Pre-built images** — never; only build-from-source.
- **Production secrets in demos** — by design; `owner` secrets are test-only.
- **Substituting the app's DB engine or internal stack** — the platform provisions what the Project declares; it never rewrites the app's dependencies.
