# Runbook — Issue #4: Register the GitHub App for Owner auth

**Goal:** create a **GitHub App** that (a) lets an Owner *sign in* with GitHub and (b) grants the platform *read-only* access to the Owner's *selected* repos so it can clone + build them at publish time. Store its credentials as platform secrets.

**Why a GitHub App, not an OAuth App (read once):** a GitHub App installs on **selected repositories** with **least-privilege, read-only `contents`**, and issues **short-lived per-installation tokens**. An OAuth App would force a blanket "read all your repos" grant. This is ADR-0008. Don't substitute an OAuth App.

This is the human half of issue #19's "sign in with GitHub" slice — the code is written to work against a fake provider in tests and against this real App when its credentials are present.

> Estimated time: ~30–45 min. Cost: free.

---

## Background: the App has TWO token types (know the difference)

This trips everyone up the first time. A GitHub App does two separate things:

| Flow | What it's for | Credentials used | Lifetime |
|---|---|---|---|
| **User-to-server (sign-in)** | "Sign in with GitHub" — identify *who the Owner is* | **Client ID + Client Secret** | the control plane only reads the user's stable numeric id once, then discards the token |
| **Installation token** | clone + build the Owner's selected repos at publish | **App ID + private key (.pem)** + installation id | ~1 hour, fetched on demand, **never stored** |

You set up **one App** that supports both. Slice 2 of #19 uses the first; slice 4 uses the second.

---

## Step 1 — Create the App

1. Go to **<https://github.com/settings/apps>** → **New GitHub App**.
   - (For a personal project, a personal-account App is fine. If you later want an org to own it, create it under the org's *Developer settings* instead — moving it later is annoying, so decide now if you have an org.)
2. **GitHub App name:** something unique, e.g. `Showcase Dev` (this shows on the consent screen Owners see).
3. **Homepage URL:** `https://showcase.dev` (or your main domain; a placeholder is fine for now).

---

## Step 2 — Identity / sign-in settings (user-to-server)

4. **Callback URL:** `https://showcase.dev/auth/github/callback`
   - This must match what the control plane uses. The code reads it from `OAUTH_CALLBACK_URL`.
   - For local testing add a second callback: `http://localhost:8080/auth/github/callback` (GitHub Apps allow multiple callback URLs).
5. ✅ Check **"Request user authorization (OAuth) during installation."**
   - This is what makes the *sign-in* flow work (user-to-server). Without it the App can only do installation tokens, not "sign in with GitHub."
6. Leave **"Expire user authorization tokens"** at its default (on). We don't store the user token anyway, so refresh doesn't matter for sign-in — we read the user id once and drop it.
7. **Setup URL (optional):** `https://showcase.dev/dashboard` — where GitHub sends the Owner after they install the App on repos. Check "Redirect on update" if you want re-installs to land there too.

---

## Step 3 — Webhook

8. **Webhook:** for the MVP you can **uncheck "Active"** (we don't need to react to GitHub events yet — we fetch what we need at publish time).
   - If you leave it active, set a **Webhook URL** (`https://showcase.dev/webhooks/github`) and a **Webhook secret** (generate a random string, save it as `GITHUB_WEBHOOK_SECRET`). Leaving it off is simpler for now; you can turn it on later.

---

## Step 4 — Permissions (least privilege)

9. **Repository permissions →**
   - **Contents: Read-only** ← this is the one that matters (clone + build source).
   - **Metadata: Read-only** (auto-selected; required).
   - Everything else: **No access.**
10. **Account permissions:** none needed.
11. **Subscribe to events:** none (webhook is off).

> ✅ AC checkpoint: *"A GitHub App exists with read-only `contents` and per-repo installation."* (Per-repo installation is the default — Owners choose "Only select repositories" when installing.)

---

## Step 5 — Where it can be installed

12. **"Where can this GitHub App be installed?"** → **"Any account"** if you want friends/other Owners to install it; **"Only on this account"** while it's just you. (ADR-0010 notes public Owners are gated on prod-grade build isolation — so keep it "Only this account" until you've done that gate.)
13. Click **Create GitHub App.**

---

## Step 6 — Generate and capture credentials

On the App's settings page after creation:

14. Note the **App ID** (shown at the top).
15. Note the **Client ID**.
16. **Client secrets → Generate a new client secret** → copy it **now** (shown once).
17. **Private keys → Generate a private key** → downloads a `.pem` file → keep it safe (shown/downloadable once).

You now have, mapping to the env vars the control plane reads:

| You captured | Env var (control plane) |
|---|---|
| App ID | `GITHUB_APP_ID` |
| Client ID | `GITHUB_APP_CLIENT_ID` |
| Client secret | `GITHUB_APP_CLIENT_SECRET` |
| Private key `.pem` contents | `GITHUB_APP_PRIVATE_KEY` |
| Webhook secret (if enabled) | `GITHUB_WEBHOOK_SECRET` |
| Callback URL | `OAUTH_CALLBACK_URL` |

---

## Step 7 — Store the credentials as platform secrets

**Never** put these in the repo, in `.env` that's committed, or anywhere a demo could read them (ADR-0008: App credentials live outside every demo trust zone).

- **Production (ADR-0009 names Azure):** put each in **Azure Key Vault** and have the VM read them at boot (managed identity → Key Vault, or inject as environment variables via the VM's systemd `EnvironmentFile` written from Key Vault). The private key is multi-line — store the whole PEM as one secret; when injecting as an env var, keep the newlines (or base64-encode it and decode on read — coordinate with the backend on which form it expects).
- **Local dev:** put them in your **uncommitted** `.env` (already git-ignored). For the private key, point at the file path or paste the PEM. If you don't set them at all, sign-in is simply disabled in dev — the rest of the app still runs.

Quick local sanity:
```bash
# from the repo, with .env filled in:
grep -E 'GITHUB_APP_(ID|CLIENT_ID)|OAUTH_CALLBACK_URL' .env   # values present, secret NOT printed in logs
```

> ✅ AC checkpoint: *"App credentials are stored as platform secrets (never in a demo trust zone)."*

---

## Step 8 — Install it on a repo (to test)

18. App settings → **Install App** → install on your account → **"Only select repositories"** → pick one test repo (e.g. a small UI repo).
19. This is what an Owner will do for each project. The control plane uses the resulting *installation* (App ID + private key → installation token) to clone that repo at publish.

> ✅ AC checkpoint: *"Installation/callback flow configured for the control plane."* (Fully exercised once #19 slice 2 ships the sign-in handler and slice 4 ships the publish/clone path — this runbook makes the App ready for them.)

---

## Done — verification checklist
- [ ] GitHub App created with **Contents: Read-only**, Metadata: Read-only, nothing else.
- [ ] "Request user authorization during installation" is **on**.
- [ ] Callback URL = `https://showcase.dev/auth/github/callback` (+ a localhost one for dev).
- [ ] App ID, Client ID, Client secret, and private key `.pem` captured.
- [ ] Secrets stored in Key Vault (prod) / uncommitted `.env` (dev) — never in git.
- [ ] Installed on at least one test repo via "Only select repositories."

## Gotchas
- **Client secret & private key are shown once.** If you lose them, regenerate (old ones can be revoked).
- **Callback URL mismatch** is the #1 sign-in failure: the value in GitHub must equal `OAUTH_CALLBACK_URL` exactly (scheme, host, path, no trailing slash).
- **OAuth App ≠ GitHub App.** If you find yourself at `Settings → Developer settings → OAuth Apps`, you're in the wrong place — use **GitHub Apps**.
- Keep the App **"Only on this account"** until the public-Owner build-isolation gate (ADR-0010) is done.
