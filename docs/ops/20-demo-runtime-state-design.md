# Runbook — Issue #20: Demo-runtime state design

**Goal:** design the five **runtime states** a Guest sees around a demo — **Booting**, **Live** (the frame/chrome around the running app), **At-capacity**, **Crashed**, and **Failed-to-start** — consistent with the public-surface design (#7), and hand them to the runtime slices.

These are designed **against working runtime behavior** (the backend for these states already exists: #9 live routing, #10 play flow, #12 capacity cap, #13 crash/failure). So you can trigger each state for real and design against it.

> Tooling: same as #7 — **Claude Design** for the visuals + **Impeccable** for in-repo refinement. See `07-visual-design-public-surface.md` → "Can I use Claude Design AND Impeccable together?" for the combined workflow; don't repeat the install here, just reuse the same project setup and the same tokens/components.

**Prerequisite:** finish #7 first (or at least its tokens + `AppFrame`/`Play` components). #20 must look like a continuation of #7, not a separate design.

---

## What each state actually is (ground truth from the codebase)

Paste the relevant bullet into the matching prompt so the design fits real behavior:

- **Booting** — after a Guest clicks Play, the Stack takes a few–tens of seconds to boot. The platform serves a dedicated **splash page** during this window (the control plane's separate "splash listener" — it deliberately shows the splash for *any* path so a half-booted demo never leaks internals). Needs progress/reassurance and an honest "this can take a moment."
- **Live** — once healthy, the Guest is routed to the running app at `https://s-<id>.run.<demo-domain>`. The design here is the **frame/chrome** *around* the Guest's app (a thin top bar): which project/author, a "this is a live sandbox" indicator, maybe time-remaining/idle hint, a "stop"/"back to portfolio" affordance, and a "restart" path. The app itself fills the rest — your chrome must be minimal and never obscure it.
- **At-capacity** — the platform caps concurrent Sessions; over the cap, Play is rejected (HTTP 503 with a Retry-After). Needs a friendly "we're at capacity, try again shortly" with a retry, not a scary error.
- **Crashed** — a Session that was live but its Stack died. The platform tears it down and shows a status page. Needs "the demo stopped unexpectedly" + a "play again" / "back to portfolio" path.
- **Failed-to-start** — the Stack never became healthy (e.g. a build/boot failure). A status page explains it failed to start, with retry / back affordances. (Owner-facing diagnostics are separate; this is the *Guest* view.)

> Tone: these are the moments most products get wrong. Make them calm, honest, on-brand, and always offer a next action. The Guest is anonymous — never expose internal ids, logs, or stack details.

---

## Claude Design prompts (one state at a time)

Start each with the **product paragraph** from `07-...md` **and** this line:
```
Reuse the existing Showcase design system/tokens and the AppFrame + Play + Button components
from the public-surface design (#7). These runtime states must look like the same product.
```

### Booting
```
Design the "Booting" state shown after a Guest clicks Play while the sandbox boots (seconds to
~a minute). Use the AppFrame. Show: the project + author, an honest progress indicator
(indeterminate is fine — booting time varies), a short reassuring line ("Spinning up a real
environment just for you…"), and a subtle sequence of what's happening (e.g. "starting services
→ waiting for the app"). Include a graceful "this is taking longer than usual" variant and a
cancel / back-to-portfolio link. Calm, confident, not a spinner-on-blank-page. Light + dark,
responsive.
```

### Live (frame / chrome)
```
Design the "Live" chrome that frames a Guest's running app. It is a THIN top bar only — the app
fills everything below and must not be obscured. Include: project name + author (link to their
portfolio), a "● Live sandbox" indicator, an optional time-remaining / idle hint, a "Restart"
control, and an exit ("Back to portfolio") control. Design its responsive/mobile form (collapse
to an icon bar). Provide a hover state for controls. It must read as "trusted platform chrome
around someone else's app", subtle and out of the way. Light + dark.
```

### At-capacity
```
Design the "At capacity" state: the platform is temporarily full and can't start a new demo
(server returns 503 with a retry hint). Friendly, not alarming: explain we cap concurrent live
demos, show a "Try again" button (and mention it'll free up shortly), and offer "Browse
portfolios" meanwhile. One clear primary action. Light + dark, responsive.
```

### Crashed
```
Design the "Crashed" state: a demo that was running stopped unexpectedly and was cleaned up.
Honest and calm: "This demo stopped unexpectedly." Primary "Play again", secondary "Back to
portfolio". No internal logs, ids, or stack traces (the Guest is anonymous). Reuse the AppFrame
so it reads as the same surface. Light + dark, responsive.
```

### Failed-to-start
```
Design the "Failed to start" state: the sandbox never came up (e.g. the project failed to
build/boot). Explain plainly that this demo couldn't start right now, offer "Try again" and
"Back to portfolio", and (optionally) a low-key "report this / contact" link. Distinct enough
from "Crashed" that the Guest understands it never started — but same visual family. No internal
diagnostics. Light + dark, responsive.
```

### Consolidate
```
Produce a single "runtime states" component set consistent with the public surface: a shared
StatusScreen (icon/illustration + headline + body + primary/secondary actions) used by
At-capacity / Crashed / Failed, plus the BootingScreen and the LiveChrome bar. List the new
components and the tokens reused. Package a handoff bundle for the React 19 + Vite app in web/.
```

---

## See each state for real (design against truth)

You can trigger these locally (runc, no gVisor needed) to screenshot and design against:
- **Booting/Live:** run the control plane + Traefik (`README.md` quickstart: `INTERNAL_HOST=0.0.0.0 … make run` then `make proxy-up`), open `/demo`, click Play — you'll see booting, then the live app at `s-<id>.run.localhost`.
- **At-capacity:** set `MAX_SESSIONS=1`, start one demo, try to start a second → 503.
- **Failed-to-start:** point a project at a bad image/build so the Stack never goes healthy.
- **Crashed:** start a demo, then kill its container (`docker rm -f`) and wait for the reaper.

Feed those real screenshots into Claude Design so the chrome fits the actual app viewport and the states match real timing.

---

## Handoff (what "done" means)

Deliver, into `web/` reusing #7's tokens:
1. `BootingScreen`, `LiveChrome`, and a shared `StatusScreen` (for At-capacity / Crashed / Failed) as React components with their states.
2. Copy (the actual user-facing text) for each state — calm, honest, anonymous-safe.
3. A note in `web/DESIGN.md` mapping each state → the runtime slice that renders it (#9 live chrome, #10 booting/play, #12 at-capacity, #13 crashed/failed).

> ✅ AC: designs for booting / live / at-capacity / crashed / failed · consistent with #7 · handed to #9/#10/#12/#13.

---

## Checklist
- [ ] #7 tokens/components exist and are reused (no separate visual language).
- [ ] All five states designed, light + dark, responsive, with real copy.
- [ ] Designed against **real** triggered states (screenshots from a local run).
- [ ] No internal ids/logs/stack details in any Guest-facing state.
- [ ] `BootingScreen` / `LiveChrome` / `StatusScreen` components + copy handed to the runtime slices.

## Gotchas
- The **Live chrome must not cover the app** — keep it a thin bar; test with a real running demo, not a mockup.
- **Booting time is variable** — design an indeterminate-but-reassuring state, and a "taking longer than usual" path, not a fake percentage.
- Keep **At-capacity** friendly (it's expected load-shedding, ADR-0006), distinct from the two **error** states.
- These are **Guest-facing**; Owner-facing build/crash diagnostics are a different surface — don't leak them here.
