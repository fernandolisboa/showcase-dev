# Context Map

The platform's bounded contexts haven't been carved yet. At this stage there is one shared glossary of system-wide language; as contexts emerge (the architecture is still firming up), each gets its own `CONTEXT.md` listed below.

## System-wide language

- [Shared glossary](./CONTEXT.md) — the core runtime nouns used across the whole platform (Owner, Guest, Project, Demo, Session, Stack, Environment).

## Contexts (anticipated — not yet carved)

- **Runner** — provisions, supervises, and tears down Sessions. The spine core.
- **Portfolio** — public per-Owner pages and customization.
- **Import** — GitHub import and Project configuration.

_(Illustrative — see `docs/agents/domain.md`. Real contexts get carved and moved under `src/<context>/CONTEXT.md` as the architecture firms up.)_

## Relationships

_TBD — recorded here as contexts are carved and their integration patterns are decided (likely via ADRs)._
