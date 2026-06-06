---
Status: accepted
---

# Demo egress: default-deny at runtime, allow-list on demand; build egress permitted

The running demo Stack is internet-facing and runs Owner code, so outbound network is a distinct attack surface — the third isolation leg, alongside resource caps (faults) and gVisor (escape), per ADR-0002. Policy:

- **Runtime: default-deny outbound.** A Session's containers communicate only within their own per-Session bridge network (UI ↔ API ↔ DB); they cannot initiate connections to the internet. This closes off crypto-mining, scanning/attacking third parties, server-side data exfiltration, and SSRF into internal networks. A Project may declare an **egress allow-list** (`host:port`) when it genuinely needs an external API; absent that, deny-all.
- **Build (publish-time): egress permitted** to fetch dependencies (npm / NuGet / apt), narrowable to registry allow-lists later. Builds are Owner-initiated and not internet-facing, so exposure is far lower than runtime's.
- **Invariants:** isolated per-Session bridge (no cross-Session traffic), platform-controlled DNS (so allow-lists are enforceable), and **never** host networking or unfiltered outbound.

This governs the demo *containers'* server-side egress — not the Guest's browser.

## Consequences

- The self-contained spine app needs no allow-list — default-deny is transparent to it.
- Per-Project egress allow-lists become a run-contract field once a demo needs outbound; until then the manifest omits it.
