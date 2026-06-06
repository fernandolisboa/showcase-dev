---
Status: accepted
---

# Demos served from a separate registrable domain, one unguessable subdomain per Session

Every Session's Stack (the demo's own UI + API + DB) is served from a **separate registrable domain** — `s-<sessionId>.run.showcasedemo.app` — never from `showcase.dev` or any subdomain of it. This is the one truly irreversible decision in the platform and must hold from day one: **the browser's same-origin / same-site policy is the enforcement.** Because `showcasedemo.app` is a different registrable domain (eTLD+1) from the app's `showcase.dev`, a fully-compromised demo is **cross-site** to the app — it physically cannot read `showcase.dev` cookies or localStorage, cannot call the control-plane API same-origin, and its requests to the app count as cross-site (so the app's `SameSite` cookies and CSRF defenses hold). Putting demos on the app's domain later would erase this wall and break every existing demo link in a security-sensitive migration — hence day-one.

## Scheme

- **App:** `showcase.dev` — Owner admin, Portfolios, auth, the control-plane API (config + Session spin-up). Demos must never reach this.
- **Demos:** `s-<sessionId>.run.showcasedemo.app`, one subdomain per Session, behind the reverse proxy → that Session's Stack. `<sessionId>` is a **high-entropy, unguessable** token — the URL *is* the capability to reach a live Session.
- **Within a Session (same-origin):** the proxy routes `/` → the UI container and `/api/*` (a declared `pathPrefix`) → the API container, all on the *one* subdomain. The UI calls **relative `/api`** — no per-Session URL injection, no CORS. UI and API are the same Owner's code; there is no trust boundary between them.
- **TLS:** a single wildcard cert for `*.run.showcasedemo.app`. `.app` is HSTS-preloaded, so HTTPS is forced and plaintext downgrade is impossible.

## Mechanism

The Runner keeps the proxy's `sessionId → Stack backend` map current as it provisions / tears down; the proxy serves a "booting…" page until the Stack's healthcheck passes, then routes to the live UI (WebSocket upgrade supported on `/api`).

## Considered options

- **Demos on `*.run.showcase.dev`** (subdomain of the app) — rejected: same registrable *site*, so domain-scoped cookies could reach demos and demo→app requests are same-site (weaker CSRF / SameSite). Cross-origin alone is not enough; we want cross-site.
- **Separate API subdomain per Session** (CORS) — rejected: forces per-Session API-URL injection into a static UI build plus Owner CORS config; the same-origin `/api` proxy avoids both.

## Action item

Register a distinct demo domain (`showcasedemo.app` is a placeholder — verify availability; the scheme stands regardless of the exact name).
