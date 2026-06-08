# Runbook — Issue #3: Register the demo domain + wildcard TLS/DNS

**Goal:** stand up a *separate* domain for running demos, with a wildcard DNS record and a wildcard HTTPS certificate, so every booted Session is reachable at `https://s-<id>.run.<demo-domain>`.

**Why a separate domain (read once):** the main app lives at `showcase.dev`; untrusted Owner code runs at the demo domain. They must be different *registrable* domains (different eTLD+1) so a demo can never set a cookie or ride an origin that affects the main app. This is ADR-0004 — don't collapse them onto one domain to save €10/yr.

You will end this runbook with:
- a registered demo domain (the codebase placeholder is `showcasedemo.app`),
- `*.run.<demo-domain>` resolving and serving a valid TLS cert,
- the domain recorded in the prod config (`DEMO_DOMAIN`).

> Estimated time: ~1 hour of work + up to 24h of DNS/registration propagation (usually minutes). Cost: ~€10–25/yr for the domain.

---

## Step 0 — Decide the two domains

You actually need **two** registrable domains for the project overall:

| Purpose | Example | HSTS-preloaded TLD? |
|---|---|---|
| Main app + Portfolios | `showcase.dev` | `.dev` is preloaded → always HTTPS |
| Demos (this issue) | `showcasedemo.app` | `.app` is preloaded → always HTTPS |

Issue #3 is **only the demo domain**. (If you haven't registered `showcase.dev` yet, do it the same way — Step 2 — but that's a different concern.)

**Pick a `.app` (or `.dev`) TLD on purpose.** Both are on the browsers' HSTS preload list, meaning browsers *refuse* to load them over plain HTTP. That's a free security upgrade and is exactly why ADR-0004 chose `.app`. Avoid `.com`/`.io` here — you'd lose the forced-HTTPS guarantee.

---

## Step 1 — Check availability

Before anything, confirm your name is free:
- Quick check: <https://instantdomainsearch.com> or your registrar's search box.
- The placeholder `showcasedemo.app` may be taken — have 2–3 alternates ready (e.g. `showcaserun.app`, `playshowcase.app`, `<yourhandle>demos.app`).

The demo domain is *user-facing in the URL bar* of every demo, so pick something short and clean, but it matters far less than the main domain.

---

## Step 2 — Register the domain (recommended: Cloudflare Registrar)

**Use Cloudflare Registrar.** Two reasons that matter for this project:
1. It sells domains at **wholesale cost** (no markup, no first-year-cheap/renewal-expensive games).
2. Your DNS will live at Cloudflare anyway (Step 3), and Traefik's automatic-certificate flow is wired for Cloudflare specifically (see `deploy/traefik/traefik.yml`). One vendor, one API token.

How:
1. Create a free account at <https://dash.cloudflare.com>.
2. **Registrar → Register Domain**, search your name, buy it. (Cloudflare Registrar requires the domain's DNS to be on Cloudflare, which it sets up automatically for names you register there.)

Acceptable alternatives if you prefer: **Porkbun** or **Namecheap** (both cheap, both fine). If you register elsewhere, you'll still want to **move DNS to Cloudflare** in Step 3 (point the registrar's nameservers at the ones Cloudflare gives you), because the cert automation depends on Cloudflare's DNS API.

> ✅ AC checkpoint: *"A demo domain distinct from `showcase.dev` is registered."*

---

## Step 3 — Set up wildcard DNS `*.run.<demo-domain>`

Every Session is `s-<id>.run.<demo-domain>`. One wildcard record covers them all.

In Cloudflare → your demo domain → **DNS → Records**, add:

| Type | Name | Content | Proxy status |
|---|---|---|---|
| `A` | `*.run` | `<your server's public IPv4>` | **DNS only (grey cloud)** |

Notes:
- `Name = *.run` becomes `*.run.<demo-domain>` — the wildcard for all Sessions.
- `Content` is the public IP of the VM that runs Traefik (the single Azure VM from ADR-0009). You'll have this after you provision the VM; if you haven't yet, do that first, then come back.
- **Turn the orange cloud OFF (DNS only).** Cloudflare's proxy (orange cloud) would terminate TLS itself and sit in front of your demos. For this project Traefik on your VM terminates TLS and enforces the per-Session isolation, so you want Cloudflare to just answer DNS, not proxy. (You *can* revisit Cloudflare-proxied mode later, but start simple and correct.)
- If you also run the main app on the same VM, add an `A` record for `@`/`www` of `showcase.dev` separately — not part of this issue.

Verify resolution (wait a few minutes first):
```bash
dig +short anything.run.<demo-domain>      # should print your server IP
dig +short s-test123.run.<demo-domain>     # same IP — wildcard matches any label
```

---

## Step 4 — Wildcard TLS certificate (Let's Encrypt DNS-01 via Traefik)

A **wildcard** cert (`*.run.<demo-domain>`) can only be issued with the **DNS-01** challenge (Let's Encrypt makes you prove you control DNS by creating a TXT record). The good news: **you don't run any of this by hand.** Traefik does it automatically and renews it, and it ships the Cloudflare DNS provider built in — so this is *config + one secret*, no scripts, no certbot.

### 4a. Create a scoped Cloudflare API token

1. Cloudflare → **My Profile → API Tokens → Create Token**.
2. Use the **"Edit zone DNS"** template.
3. Scope it to **your demo domain only** (Zone Resources → Include → Specific zone → `<demo-domain>`).
4. Create it and copy the token **once** (you can't see it again).

This token lets Traefik create the temporary `_acme-challenge` TXT records Let's Encrypt asks for. Least privilege: DNS edit on one zone, nothing else.

### 4b. Give Traefik the token + enable HTTPS in prod

The local Traefik config (`deploy/traefik/traefik.yml`) is HTTP-only by design. For prod you add a `websecure` (:443) entryPoint and an ACME resolver. The file already documents this — here's the prod overlay to apply on the VM (keep the dev file unchanged; deploy a prod variant):

```yaml
# additions for the PROD traefik.yml
entryPoints:
  web:
    address: ":80"
    http:
      redirections:           # force HTTP → HTTPS (belt-and-braces; .app is HSTS too)
        entryPoint:
          to: websecure
          scheme: https
  websecure:
    address: ":443"

certificatesResolvers:
  letsencrypt:
    acme:
      email: "you@example.com"          # Let's Encrypt expiry notices go here
      storage: "/etc/traefik/acme.json" # persist the cert across restarts (mount a volume!)
      dnsChallenge:
        provider: cloudflare
        resolvers: ["1.1.1.1:53"]
```

Then make the wildcard cert apply to Session routers. The control plane already hands Traefik its routing map; the cert is attached via the TLS option on the `websecure` entryPoint. The simplest robust setup is a default wildcard cert request in the dynamic config the control plane serves — coordinate this with the backend (it's a one-line `tls.domains` entry: main `*.run.<demo-domain>`). If you're doing this before that wiring exists, you can pin a static cert request in the static config:

```yaml
# optional: request the wildcard up front so it exists before the first Session
tls:
  stores:
    default:
      defaultGeneratedCert:
        resolver: letsencrypt
        domain:
          main: "*.run.<demo-domain>"
          sans: ["run.<demo-domain>"]
```

Pass the token in the environment (compose.yml `environment:` or the systemd unit):
```bash
CF_DNS_API_TOKEN=<the token from 4a>
```
The Traefik comment in `deploy/traefik/traefik.yml` already names this exact variable.

> ⚠️ **Persist `acme.json`.** Mount it on a volume and `chmod 600`. If you lose it on every redeploy you'll re-issue certs and can hit Let's Encrypt rate limits (5 duplicate certs/week). For experiments, point `acme.caServer` at the Let's Encrypt **staging** endpoint first, confirm it works, then remove that line for the real cert.

### 4c. Verify the cert

```bash
curl -sI https://s-test123.run.<demo-domain> | head -n1     # expect an HTTP status, not a TLS error
echo | openssl s_client -servername s-test123.run.<demo-domain> \
  -connect s-test123.run.<demo-domain>:443 2>/dev/null | openssl x509 -noout -subject -dates
# subject should be *.run.<demo-domain>, dates valid
```

> ✅ AC checkpoint: *"`*.run.<demo-domain>` resolves and serves a valid wildcard TLS cert."*

---

## Step 5 — Record the domain in project config

The control plane reads the demo domain from the environment (`internal/config/config.go` → `DEMO_DOMAIN`, default `localhost`), and the URL scheme from `DEMO_SCHEME`.

In your **prod** environment (the VM's `.env` / systemd `EnvironmentFile` / Azure config — *not* committed):
```bash
DEMO_DOMAIN=<demo-domain>      # e.g. showcasedemo.app  (NO scheme, no *.run prefix)
DEMO_SCHEME=https              # prod MUST be https (main.go warns loudly if it isn't)
ENV=prod
```

`.env.example` in the repo documents these; copy it to `.env` and fill real values. Never commit the real `.env`.

> ✅ AC checkpoint: *"Recorded as the demo domain in project config."*

---

## Done — verification checklist

- [ ] Demo domain registered (distinct eTLD+1 from `showcase.dev`).
- [ ] `dig +short s-anything.run.<demo-domain>` returns your VM IP.
- [ ] `https://s-test.run.<demo-domain>` serves a valid `*.run.<demo-domain>` cert (no browser warning).
- [ ] `DEMO_DOMAIN` + `DEMO_SCHEME=https` set in prod config; `acme.json` persisted on a volume.

## Gotchas
- **Wildcard ≠ HTTP-01.** If you ever see Traefik trying an HTTP-01 challenge for the wildcard, it will fail — wildcards *require* DNS-01. Make sure `dnsChallenge` is set, not `httpChallenge`.
- **Orange cloud on the wildcard** breaks the model (Cloudflare terminates TLS) — keep it grey (DNS only).
- **Rate limits:** use the staging CA while iterating.
- The main domain `showcase.dev` is a separate registration; do it the same way but it's outside this issue.
