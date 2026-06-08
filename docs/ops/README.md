# Ops runbooks (human tasks)

Step-by-step guides for the `ready-for-human` issues — the manual ops + design
tasks the agent can't do for you. Each maps 1:1 to a GitHub issue.

| Runbook | Issue | What you do | Type |
|---|---|---|---|
| [03 — Demo domain + wildcard TLS/DNS](03-register-demo-domain-tls-dns.md) | #3 | register the demo domain, wildcard DNS + Let's Encrypt wildcard cert (Cloudflare + Traefik) | ops |
| [04 — Register the GitHub App](04-register-github-app.md) | #4 | create the GitHub App for Owner sign-in + read-only repo access; store secrets | ops |
| [07 — Visual design: public surface](07-visual-design-public-surface.md) | #7 | design Landing / Portfolio / Project pages with Claude Design + Impeccable; hand off tokens | design |
| [20 — Demo-runtime state design](20-demo-runtime-state-design.md) | #20 | design booting / live / at-capacity / crashed / failed states | design |

Suggested order: **#7 first** (its tokens/components feed #20 and #19), then **#3**
and **#4** whenever you provision the prod VM / wire sign-in, then **#20**.

The two design runbooks share one workflow — read #7's "Can I use Claude Design
AND Impeccable together?" section once; #20 reuses the same setup.
