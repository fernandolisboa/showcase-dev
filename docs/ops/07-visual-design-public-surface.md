# Runbook — Issue #7: Visual-design pass, public surface

**Goal:** design the three static public pages — **Landing**, **Portfolio** (`showcase.dev/{username}`), and **Project page** — and hand off reusable **design tokens + components** that the functional slices (#10 play flow, #19 Owner/Portfolio) build against.

This is a design task, done with AI design tooling, that runs in parallel with the backend. It does **not** depend on the Runner. The deliverable is *design + tokens/components*, not finished production pages (those get wired up in #19).

> Tooling: **Claude Design** (primary, visual exploration) **+ Impeccable** (in-repo refinement & anti-"AI-slop" guardrails). They compose — see the workflow below.

---

## The product, in one paragraph (paste this into every design prompt)

> Showcase is a platform for per-user developer **portfolios** whose differentiator is **live-running projects**: a visitor clicks **"Play"** on a project and a real full-stack app boots in an isolated sandbox and runs right in the browser. The audience is **developers**. The public surface is three pages: a **Landing** page that sells the "play a real running app, not a screenshot" idea; a **Portfolio** at `showcase.dev/{username}` listing one developer's projects; and a **Project page** for a single project with the prominent **Play** affordance.

Design principles to state to the tools: developer-grade, fast, restrained, high-contrast, content-first; the **Play** button is the hero interaction everywhere; dark-mode-first is a reasonable default for a dev audience (offer both).

---

## Can I use Claude Design AND Impeccable together? Yes.

They operate at **different stages** and don't conflict:

| | Claude Design (Anthropic Labs, web app) | Impeccable (`pbakaus/impeccable`, Claude Code skill) |
|---|---|---|
| Where | claude.ai web app (Pro/Max/Team) | in your repo, via Claude Code |
| Does | explores look-and-feel, generates prototypes/mockups, builds a design system from prompts + your codebase, exports a **handoff bundle** | gives the agent a *design vocabulary*, refines real components as **diffs in your codebase**, audits for "AI slop", can gate CI |
| Output | visual direction + handoff bundle for Claude Code | reviewable code diffs + `PRODUCT.md` + slop rules |

**Recommended combined workflow:**

1. **Diverge in Claude Design.** Explore 2–3 directions for the visual language and the three pages (prompts below). Pick one. Let it build a design system (colors, type, components).
2. **Export the handoff bundle** from Claude Design ("ready to build → handoff to Claude Code").
3. **Bring it into the repo** (`web/`, React 19 + Vite). Hand the bundle to Claude Code with the single handoff instruction Claude Design gives you.
4. **Install Impeccable** in the repo and initialize it so it learns *this* project's tokens (it inherits, doesn't overwrite):
   ```
   /plugin marketplace add pbakaus/impeccable
   /plugin           # then install "impeccable"
   /impeccable init  # discovery interview → writes PRODUCT.md at repo root
   ```
   (Alternatives: `npx impeccable skills install`, or `npx skills add pbakaus/impeccable`.)
5. **Refine with Impeccable** against the imported design — e.g.
   ```
   /impeccable polish    the landing hero
   /impeccable typeset   the Portfolio project list
   /impeccable audit     the Project page
   ```
   Impeccable edits the real components and explains each change.
6. **Lock quality in CI (optional but recommended):** wire Impeccable's slop detector into the `web` job:
   ```
   npx impeccable detect src/      # deterministic rules, exit code the build can read
   ```

> Rule of thumb: **Claude Design decides what it looks like; Impeccable makes the built version not look AI-generated and keeps it that way.** Use Claude Design for exploration and the system; use Impeccable for in-code craft and guardrails. If you only had one: Claude Design for greenfield look, Impeccable for refining what's already coded.

---

## Claude Design prompts (copy-paste, one page at a time)

Start each session by pasting the **product paragraph** above, then the page prompt. Iterate with Claude Design's inline comments / adjustment knobs.

### Prompt 0 — establish the system first
```
You are designing the public marketing+portfolio surface for "Showcase" (paragraph above).
First, propose a design system for a developer audience: a color palette (dark-first, with a
light variant), a type scale (one display face + one readable UI/mono pairing), spacing scale,
radius, and elevation. Keep it restrained and high-contrast — think Linear / Vercel / Stripe
docs, not a flashy SaaS landing. Output the tokens as CSS custom properties and a short
rationale. Then we'll design pages against this system.
```

### Prompt 1 — Landing page
```
Design the Landing page for Showcase using the system above. Goal: convince a developer that
projects here are REAL running apps they can play, not screenshots. Sections: (1) a hero with a
one-line value prop and a primary "Play a live demo" call-to-action plus a secondary "Browse
portfolios"; the hero should visually imply a running app (e.g. a framed app window with a Play
overlay). (2) a 3-step "how it works" (click Play → a sandbox boots → it runs in your browser).
(3) a strip of example portfolios/projects. (4) a footer. Responsive: design mobile, tablet,
desktop. Make the Play affordance the clear hero. Show light and dark.
```

### Prompt 2 — Portfolio page (`showcase.dev/{username}`)
```
Design the Portfolio page at showcase.dev/{username}: one developer's public profile listing
their projects. Include a profile header (avatar, name/handle, short bio, links) and a
responsive grid of Project cards. Each card needs: project name, one-line description, the tech
stack (small badges), a thumbnail/preview, and a prominent "Play" button (the hero interaction).
Design the EMPTY state (no projects yet) and the loading state. Cards must make "this is a
runnable app" obvious. Responsive grid: 1 col mobile → 2–3 desktop. Light and dark.
```

### Prompt 3 — Project page
```
Design the single Project page. Above the fold: project name, author (links to their portfolio),
full description, tech stack, and a large primary "Play" button that will boot a live sandbox.
Include: a preview/screenshot area, a "what you'll see when it boots" note, repo link(s), and
secondary metadata. Design the Play button's hover/active/disabled states. This page sets the
expectation right before a real app boots — make Play unmistakable and reassuring (it spins up a
real environment). Responsive. Light and dark.
```

### Prompt 4 — extract the tokens/components for handoff
```
Finalize the design system as portable tokens (CSS custom properties) and a component inventory
(Button incl. the Play variant, Card, Badge, ProfileHeader, AppFrame, layout primitives) with
their states. Package a handoff bundle for Claude Code targeting a React 19 + Vite + TypeScript
app in a `web/` directory. List the tokens, the components, and which page uses which.
```

> Keep the **Play** button, the **Project card**, and the **AppFrame** (the framed running-app visual) consistent across all three pages — they're reused in #20's runtime states.

---

## Handing off to the codebase (what "done" means)

The frontend is `web/` (React 19 + React Router 7 + Vite + TypeScript), built to `internal/web/dist` and embedded in the Go binary. Deliver:

1. **Design tokens** as CSS custom properties (e.g. `web/src/styles/tokens.css`) — colors, type scale, spacing, radius, shadows, both themes. This is the single source of truth #19/#10 import.
2. **A small component library** (`web/src/components/`): at minimum `Button` (with a `Play` variant), `Card`/`ProjectCard`, `Badge`, `ProfileHeader`, `AppFrame`, and layout primitives — each with its states, matching the design.
3. **The three pages** as components/routes the functional slices fill with real data (#19 wires Portfolio/Project to the API; #10 wires Play to `POST /api/play`).
4. A short **`web/DESIGN.md`** (Impeccable can generate this) describing tokens + components so the backend slices consume them without re-deriving the design.

> ✅ AC: responsive Landing/Portfolio/Project designs · the Play affordance + Project-listing visuals defined · tokens + components handed to #10/#19.

---

## Checklist
- [ ] Design system (tokens) decided in Claude Design; light + dark.
- [ ] Landing, Portfolio, Project designed, responsive (mobile/tablet/desktop), with empty/loading states where relevant.
- [ ] Handoff bundle imported into `web/`; Impeccable initialized (`/impeccable init` → `PRODUCT.md`).
- [ ] `tokens.css` + component library + three page components committed.
- [ ] (Optional) `npx impeccable detect src/` wired into the `web` CI job.
- [ ] `web/DESIGN.md` handed to the #10/#19 slices.

## Notes
- This can start **now** — it doesn't need the backend. The functional slices build *to* this design, so doing it early de-risks #19.
- Don't over-build interactivity here; the slices wire data/behavior. Deliver the *look*, the *tokens*, and the *components*.
- Reuse from here flows into **#20** (runtime states) — keep the AppFrame/Play visuals stable.
