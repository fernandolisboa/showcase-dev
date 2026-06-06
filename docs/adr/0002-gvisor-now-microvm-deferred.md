---
Status: accepted
---

# Sessions sandboxed with gVisor + resource caps from day one; microVMs (Kata) deferred

Demos are internet-facing, so the real adversary is anyone who reaches a running Demo and exploits a bug in the Owner-authored (possibly AI-generated) app — not a malicious author. "It's my own code" is therefore not a security boundary. Every Session runs under the **gVisor (`runsc`) runtime** with strict per-Stack resource caps (`--memory`, `--cpus`, `--pids-limit`, disk quota), non-root, dropped capabilities, and a read-only root filesystem. gVisor is chosen over plain Docker (an internet-exposed container is one kernel bug from the host) and over Kata/Firecracker microVMs (whose VMM + guest-kernel + per-VM-networking ops cost is unjustified for a solo maintainer at single-digit users); it keeps the Compose Stack substrate unchanged via `--runtime=runsc`.

## Deferred upgrade

When the platform takes on **untrusted users at scale** — public sign-up, or any meaningful population of unvetted authors — upgrade Session isolation from gVisor to **hardware-virtualised microVMs (Kata Containers + Firecracker)**. The Runner interface (ADR-0001) is the swap point. This is an isolation *upgrade* from an already-sandboxed baseline — not a correction of an exposure — and needs no change to routing, the run contract, or demo links.

## Consequences

- Isolation has three independent legs, tracked separately: **resource caps** (fault / runaway), **gVisor** (escape), **egress policy** (outbound abuse — decided separately). microVMs would harden only the escape leg.
- gVisor has syscall-compatibility gaps and IO overhead; each Project must smoke-test clean under `runsc` as part of its run contract.
