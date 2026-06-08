---
Status: accepted
---

# Runtime egress allow-list: a per-Session, dual-homed, allow-list-only proxy sidecar

ADR-0007 set runtime egress to default-deny and said a Project may declare an egress allow-list (`host:port`) "once a demo needs outbound". #18 enforced and regression-guarded the default-deny / no-host-networking / build-egress invariants but deliberately did **not** build the allow-list. This ADR records how the allow-list is enforced when a Project declares one — without weakening default-deny for everything else.

The running demo Stack is internet-facing and runs untrusted Owner code (ADR-0002: "it's my own code" is not a boundary). So the allow-list cannot be a convenience an app can widen: a malicious app that tries to reach a non-allowed host — or that ignores the mechanism entirely — must still get nothing.

## Decision

Keep the per-Session network **`internal: true`** (ADR-0007). Routelessness stays the real enforcement: an app on a network with no default route cannot reach the internet, full stop — independent of any proxy.

When **and only when** a Project declares a non-empty egress allow-list, the compiler adds:

- A **second, non-internal network** (`<project>-egress-net`) — the only segment in the Stack with a route out.
- A **per-Session egress-proxy sidecar** (`egress`, a reserved service name) **dual-homed** on both the sealed internal network and the egress network. It is the single bridge between the routeless apps and the outside.
- `HTTP_PROXY` / `HTTPS_PROXY` (and lowercase) env on every Owner service, pointing at the sidecar on the internal net (`http://egress:8888`) — the **convenience path** so a well-behaved app's stdlib HTTP client just works. Plus `NO_PROXY` covering intra-Stack destinations (`localhost,127.0.0.1,db,<service-names>`) so app→db and ui→api traffic never pointlessly traverses the proxy.

Apps stay on the internal net **only**; they never join the egress net. The sidecar is the only member of the egress net. So:

- A well-behaved app reaches allowed hosts via the injected proxy env.
- A malicious app that **ignores** the proxy and dials an IP directly still has **no route** — it is routeless on the internal net. The proxy is a convenience, not the enforcement boundary; the boundary is L3 routelessness.
- A malicious app that **uses** the proxy but asks for a non-allowed `host:port` is refused by the proxy (default-deny, see below).

### The proxy: allow-list-only, never an open relay

The sidecar is reachable by untrusted Owner code, so it must never forward to anything not explicitly declared. It is a small, auditable Go forward proxy (`cmd/egress-proxy`) — Go is already the platform language, the "never an open relay" property must be readable in a few dozen lines, and per-`host:port` allow-listing is something off-the-shelf forward proxies don't do cleanly (tinyproxy filters by host regex and caps CONNECT ports globally; Squid needs heavy ACL config). It:

- Reads the allow-list from a single env var, `EGRESS_ALLOWLIST=host1:443,host2:80` — the **rendered form of the already-validated** `Egress []EgressRule`. The run contract is the source of truth for what is valid; the proxy is just a renderer/enforcer of a list the compiler already validated. The proxy **default-denies on any parse error**.
- Forwards plain HTTP only to an allowed `host:port`, and tunnels HTTPS via **`CONNECT`** only to an allowed `host:port`.
- Checks the **post-parsed** destination `host:port` (never the raw line, never the `Host:` header) against the allow-list, so sloppy-parsing and Host-rewriting bypasses don't apply.
- **Resolves allow-listed hostnames itself on the egress net.** The app has no DNS path out; only the proxy resolves and dials. The allow-list is matched on the hostname the client asked for, before resolution — a poisoned DNS answer can only redirect an *already-allowed* name, not widen the list.
- Logs denials by destination only — never request path or body, so the audit trail isn't a side channel.

### Platform-reserved keys

`egress` is a reserved service name (joining `db`/`seed`). `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (and lowercase) are **platform-reserved env names**: an Owner manifest may not set them. Platform wins — these keys are how the platform points apps at the sidecar, so letting Owner code override them would let an app redirect or disable its own egress routing. Rejected at validation, mirroring the reserved-service-name guard.

### Modeling note

`ExecutionPlan.Network` stays a single value (the per-Session internal bridge — the common case). The egress net is an **optional** `EgressNetwork *NetworkSpec`, nil unless an allow-list is declared. This keeps the one-network mental model intact for every Stack that doesn't opt into egress, at the cost of a second nullable field rather than a `[]NetworkSpec`. The proxy image is **platform-owned**, supplied via `runner.Options` (like the Traefik container name), not via the per-Project image map.

## Considered options

- **Custom Go forward proxy, dual-homed sidecar** *(chosen)* — exact `host:port` allow-list + `CONNECT`, auditable "never an open relay", no new infra, hardened like any app service by the existing compiler baseline.
- **tinyproxy** — no build, but filters by host *regex* and caps CONNECT ports *globally*; can't express per-host `host:port`. Weaker than the spec requires.
- **Squid** — precise `host:port` ACLs, but a heavy image and config to render for a one-list policy.
- **Per-app firewall rules instead of a proxy** — would need NET_ADMIN / iptables in the app containers, the opposite of cap-drop-ALL; rejected.

## Consequences

- Egress is opt-in and additive: a Stack with no allow-list is byte-for-byte the default-deny Stack of ADR-0007 — no second network, no sidecar, no proxy env. The self-contained spine app is unaffected.
- The proxy sidecar is hardened by the same compiler baseline as app services (non-root, cap-drop ALL, read-only rootfs, no-new-privileges) plus a small resource cap and a healthcheck — it is trusted platform code, but reachable by untrusted code, so it is locked down as if it weren't.
- The allow-list is `host:port` granular and validated at compile time; the proxy never becomes the source of truth for validity.
- Residual: the allow-list is by hostname/IP, not by URL path or method — an allowed host is reachable for any path. Narrowing to path/method, and narrowing build egress to a registry allow-list (ADR-0010), are separate future tightenings.
