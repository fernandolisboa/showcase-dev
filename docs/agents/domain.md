# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

**This repo uses a multi-context layout** (monorepo): a `CONTEXT-MAP.md` at the root points to one `CONTEXT.md` per bounded context.

## Before exploring, read these

- **`CONTEXT-MAP.md`** at the repo root — it points at one `CONTEXT.md` per context. Read each one relevant to the topic you're about to work on.
- The per-context **`CONTEXT.md`** files it references (typically `src/<context>/CONTEXT.md`).
- **`docs/adr/`** at the root for system-wide decisions, and **`src/<context>/docs/adr/`** for context-scoped decisions. Read the ADRs that touch the area you're about to work in.

If any of these files don't exist yet, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The producer skills (`/ubiquitous-language`, `/grill-with-docs`) create them lazily when terms or decisions actually get resolved.

## File structure

```
/
├── CONTEXT-MAP.md                       ← index of contexts (root)
├── docs/adr/                            ← system-wide decisions
└── src/
    ├── runner/                          ← illustrative; actual contexts TBD
    │   ├── CONTEXT.md
    │   └── docs/adr/                    ← context-specific decisions
    ├── portfolio/
    │   ├── CONTEXT.md
    │   └── docs/adr/
    └── import/
        ├── CONTEXT.md
        └── docs/adr/
```

The context names above are illustrative. The actual bounded contexts get pinned by `/ubiquitous-language` and `/grill-with-docs` as the architecture firms up.

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in the relevant `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in any glossary yet, that's a signal — either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/grill-with-docs`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders) — but worth reopening because…_
