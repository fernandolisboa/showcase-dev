---
Status: accepted
---

# Owner auth is GitHub-only (GitHub App); Guests are anonymous

For the MVP, the sole Owner authentication provider is **GitHub**, via a **GitHub App** (not a broad OAuth App). The audience is developers — all have GitHub — and the platform already requires a GitHub grant to clone and build Owner repos (the run contract, ADR-0003), so reusing that grant for sign-in adds no new auth surface, no password storage, and a single consent. No email/password and no other identity providers in the MVP.

A GitHub App is chosen over an OAuth App for least privilege: the Owner installs it on **selected repos** with **read-only contents** and short-lived tokens, rather than granting a blanket read-all-repos token. These installation tokens are platform secrets and never enter a demo (ADR-0003).

**Guests are anonymous** — no authentication; they browse Portfolios and play Demos without an account.

## Consequences

- Identity is coupled to GitHub for the MVP; non-GitHub Owners are out of scope until other providers / import sources arrive (deferred alongside the §9 import roadmap).
- The public **showcase username** (the Portfolio URL, ADR-0005) defaults to the Owner's GitHub handle but is stored decoupled from it, so a GitHub rename doesn't break Portfolio URLs.
