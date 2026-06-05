# Dev Showcase — MVP Planning Seed

> **Status: seed, not source of truth.** This captures the decisions, rationale, scope, and open questions from the exploration session. It is the **input** for:
> - `to-prd` → generates the formal PRD on the issue tracker
> - `grill-with-docs` → resolves the open questions into `CONTEXT.md` terms and ADRs
>
> A fresh Claude Code session won't have the chat that produced this. This doc is the bridge.

---

## 1. What it is

A platform for **per-user developer portfolios** whose differentiator is **live-running projects**: a guest opens a developer's page, clicks "play" on a project, and a real, full-stack application boots in an isolated environment and runs interactively in the browser. Working name/domain: `showcase.dev`.

## 2. The core bet

**Live execution of real applications is the product.** Landing pages, customization, and import flows are undifferentiated portfolio tooling that many tools already do. The hard, defensible part is "click play, the real thing runs." Everything else hangs off that and is sequenced behind it.

## 3. Scope philosophy

- **Spine-first.** The MVP is *not* a feature set; it's the thinnest end-to-end slice that proves the core claim. Layers are added behind a working spine, in dependency order.
- **Personal use first**, invite a trusted few later, **no public launch / monetization yet** — but no decision may make later hardening a rewrite. (See §6.)

## 4. The spine (first vertical slice)

> A guest opens a public portfolio page → sees a real full-stack app (the founder's own: **Angular UI + .NET API + Postgres**) → clicks play → it boots in an isolated environment → they interact with a live, working system → it tears down on idle.

Get this working for **one hand-configured project**. That is the differentiator proven. Customization, auto-detection, zip upload, and mocking are explicitly **not** in the spine.

## 5. Execution model (decided)

- **Run real backends server-side** — not browser-only/WebContainers. Browser execution (Node-only) was rejected because it can't run .NET/Go/Python or real databases, which is the whole point here.
- **Run contract is explicit, not magic.** Each project ships its own `Dockerfile`(s). A linked UI+API pair ships a `compose` file / small manifest declaring the services, their ports, and which is UI vs API. **No auto-build in the MVP.** (Auto-detect/auto-build deferred — see §9.)
- **Two-repo linking:** the owner designates a UI repo + API repo(s) in their admin area; the platform composes them (inject API URL into UI env, stand up a DB alongside).
- **Substrate:** one **Docker Compose stack per project**, spun up **on demand** on a VM, fronted by a **reverse proxy** that gives each live session its **own subdomain**, with **idle-timeout teardown**. **Orchestration sits behind an interface** so the runtime (plain Docker today) can be swapped for a stronger sandbox later without a rewrite.
- Firecracker/microVMs were the *previous* plan's default; they were a response to *untrusted code at scale*, which is overkill for trusted code and a handful of users. Held as a deferred upgrade (§6).

## 6. Security — bake-in-now vs. defer

**Structural isolation (decide NOW — cheap now, expensive/impossible to retrofit):**

- **Demos run on a separate registrable domain from the app** (e.g. `*.run.<domain>.app`), never on `showcase.dev`. Untrusted demo code then physically cannot read app cookies, localStorage, or hit the API same-origin. (Pattern: GitHub→`githubusercontent.com`, CodeSandbox→`csb.app`.) Moving origins later breaks all links and is a security-sensitive migration — so this is fixed from day one.
- **Container launch hygiene:** non-root user, dropped Linux capabilities, no `--privileged`, read-only root FS + tmpfs for writes, **never mount the Docker socket** into a demo.
- **Resource caps per stack:** `--memory`, `--cpus`, `--pids-limit` so a runaway demo can't DoS the host.
- **No unrestricted egress by default:** demos on a controllable bridge network; may stay permissive initially but never host networking / unfiltered outbound.
- **Secrets boundary:** demo containers get only their own scoped env (generated DB creds for that stack). Platform secrets (GitHub tokens, DB) never share a trust zone with user code. The orchestrator/control plane is never reachable by demo code — demos talk to the proxy, not the spin-up API.
- **App auth hygiene:** httpOnly/secure/SameSite cookies scoped to the app origin only (reinforced by the domain split above); CSRF protection on management routes.

**Operational hardening (deferred until untrusted users):**

- Swap plain Docker for **gVisor or Firecracker microVMs** (kernel-level isolation for hostile code) — enabled by the "orchestration behind an interface" decision.
- Fine-grained per-domain **egress proxy**, WAF, audit logging, abuse/anomaly monitoring.

## 7. Multi-tenancy & routing (decided / recommended)

- **Product landing** at `showcase.dev` — **minimal for MVP**: what-it-is + sign in. **Defer** the featured-users carousel and pricing tabs until there are real users and a business model. Reserve the route now.
- **Public portfolios** at `showcase.dev/{username}` (**path-based for MVP**). Subdomains (`{username}.showcase.dev`) are a later branding upgrade — they add wildcard-cert and cookie-scoping cost to the main app for no MVP benefit. (This is separate from the *demo* wildcard domain in §6.)
- **Reserved-username list** so usernames can't collide with product routes (`/about`, `/pricing`, `/login`, `/api`, `/dashboard`, …).
- **Per-user admin/management routes** behind auth, for portfolio + repo/project configuration.

## 8. Customization (decided)

- **MVP — C1:** theme/preset selection, section toggles (about / contact / fun-facts), editable structured content (text/images/links), accent color. **Pages ship static and hardcoded with the spine; the C1 engine is built LAST in the MVP**, retrofitted onto those pages. (Customization is a leaf — it arranges content that must already exist and gates nothing.)
- **Post-MVP — C2:** section composer (reorderable predefined blocks: hero, project grid, about, markdown, embed). **Includes a constrained "custom HTML/CSS" block** — ⚠️ user-authored markup is an XSS surface: must be allow-list-sanitized **and** rendered in a sandboxed iframe with strict CSP, scoped so it can't touch app chrome or exfiltrate via CSS.
- **Out (for now) — C3:** free-form drag-and-drop builder. Explicitly rejected as scope creep / a separate product.

## 9. Import sources

- **MVP:** GitHub only (OAuth/App, read-only repo access).
- **Post-MVP:** zip upload (new trust boundary — untrusted-archive handling); **auto-detection / auto-build** (inspect repo, *generate* a Dockerfile the owner reviews — convenience layer, never a phase-1 dependency); mocking for UI-only repos (someone must author the fake responses, e.g. owner-supplied MSW handlers).

## 10. Phasing (indicative)

| Phase | Goal |
|---|---|
| 0 | Foundation: repo, monorepo layout, local dev, CI, agent docs, initial ADRs |
| 1 | Auth (GitHub OAuth) + **static** public profile/project pages (the demo's home) |
| 2 | GitHub import + project model + the explicit **run contract** (Dockerfile/compose + two-repo linking) |
| 3 | **The runner** — on-demand compose spin-up, reverse-proxy + per-session subdomain on the separate demo domain, idle teardown, resource caps. *This is the spine core.* |
| 4 | **C1 customization** retrofitted onto the static pages |
| Deferred | auto-build, zip upload, UI-only mocking, C2 (incl. custom HTML/CSS), C3, operational security hardening |

## 11. Tech stack — DEFERRED (resolve in-repo, early ADR)

Intentionally **not** pre-decided. Constraints that bound the choice:
- Must orchestrate Docker/compose lifecycles + a reverse proxy + per-session wildcard subdomains.
- Founder fluency: C#/.NET, TypeScript/Angular, Node, Postgres, Redis, Azure.
- Solo maintainer → favor boring, well-supported, low-operational-surface tech.
- The previous attempt used Rust/Axum + Next.js 15 + Postgres/Neon + Redis/Upstash + Cloudflare R2 + Auth.js v5 + Stripe — reference only; **re-evaluate from the constraints, don't inherit by default.**

## 12. Open questions → resolve with `grill-with-docs` (as ADRs)

- Auth scope (GitHub-only?) and **how a project declares the env vars / secrets** it needs.
- **Session lifecycle:** concurrency cap, max run time, crash handling, per-stack resource limits.
- **DB seeding:** does a demo need a seeded database to be worth showing? (Founder's app: migrations + seed baked into the compose.)
- **Egress policy** for demo containers (default-deny + allow-list?).
- **Final routing call** (path vs subdomain for portfolios) + the demo domain name.
- Shareable / persistent demo links?

## 13. Tooling & workflow

- **Issue tracker: GitHub Issues** — native to the engineering skill chain via the `gh` CLI.
- **Skill sequence in the new repo:**
  1. `setup-matt-pocock-skills` — wire up tracker + triage labels + domain-doc layout (likely **multi-context**, monorepo)
  2. `to-prd` — turn this seed into the formal PRD on the tracker
  3. `ubiquitous-language` — seed `CONTEXT.md` (terms to pin: *project, showcase, demo, runner, session, environment, stack, owner, guest*)
  4. `grill-with-docs` — harden the plan against `CONTEXT.md`, write ADRs for the irreversible calls (execution model, run contract, domain split)
  5. `to-issues` — slice into tracer-bullet vertical slices
  6. `tdd` / `diagnose` / `review` / `qa` during implementation

## 14. Non-goals (for now)

Public-launch readiness, horizontal scale, billing/monetization, anonymous untrusted users at scale. Revisit when the spine works and real users arrive.
