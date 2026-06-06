# Showcase — Shared Glossary

System-wide language for the Showcase platform — the core nouns used across every context. Context-specific terms live in their own `CONTEXT.md` once those contexts are carved (see `CONTEXT-MAP.md`).

## Language

### Actors

**Owner**:
The authenticated developer who configures Projects and publishes them on their Portfolio.
_Avoid_: User, account, developer

**Guest**:
An unauthenticated visitor who browses an Owner's Portfolio and plays its Demos.
_Avoid_: Visitor, viewer, user

### Public surface

**Portfolio**:
An Owner's public page, aggregating their Projects — what a Guest browses at `showcase.dev/{username}`. One per Owner.
_Avoid_: Profile, page, showcase

**Showcase**:
The product and platform as a whole (the brand; `showcase.dev`) — not an object inside the domain. An Owner's public page is a Portfolio, never "a Showcase."

### Runtime model

**Project**:
A runnable application an Owner has configured — its source repo(s) plus the run contract describing how to boot it. Static configuration, not itself running.
_Avoid_: App, repo, demo

**Run contract**:
The explicit declaration of how a Project boots — the Dockerfile(s) it ships plus a manifest describing its services, database, env, and seed step. The platform builds and runs strictly from this and never infers it.
_Avoid_: Config, spec, descriptor

**Demo**:
The Guest-facing offering that a Project is playable — the "play" affordance on a Project. One per Project; starting it opens a Session.
_Avoid_: Sandbox, instance, session

**Session**:
One Guest's single interactive run of a Project — the ephemeral unit of execution, with its own subdomain and a boot → idle → teardown lifecycle.
_Avoid_: Instance, run, demo

**Stack**:
The set of services (UI, API, database, …) a Session runs, as declared by the Project's compose. Exactly one Stack per Session.
_Avoid_: Deployment, cluster, compose

**Environment**:
The isolation boundary — network, resource caps, filesystem — that wraps a single Session's Stack.
_Avoid_: Sandbox, container, box

**Runner**:
The platform subsystem responsible for a Session's lifecycle — provisioning it, supervising it, and tearing it down. The spine core.
_Avoid_: Orchestrator, executor, engine
