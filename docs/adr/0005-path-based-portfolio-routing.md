---
Status: accepted
---

# Portfolios are path-based (`showcase.dev/{username}`); subdomain portfolios deferred

Public Portfolios are served path-based at `showcase.dev/{username}` for the MVP. Per-username subdomains (`{username}.showcase.dev`) are a deferred branding upgrade — they'd push wildcard-cert management and cookie-scoping complexity onto the *main app* for no MVP benefit. Because usernames share the path namespace with product routes, a **reserved-username list** (`about`, `login`, `api`, `dashboard`, `pricing`, …) prevents collisions.

## Consequences

- Moving to subdomain Portfolios later changes Portfolio URLs — a cosmetic / SEO migration, not a security-sensitive one (unlike the demo-domain split, ADR-0004).
- The reserved-username list must be enforced at username creation and kept in sync with product routes as they're added.
