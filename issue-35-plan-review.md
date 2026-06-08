# Issue #35 plan review — per-Session egress proxy

Read the plan carefully against the codebase. The architecture is sound and matches #18/ADR-0007 precisely. The custom Go proxy choice is correct — tinyproxy and Squid both fail the spec for the reasons given. But a few things in the change list are off or under-specified.

## Where the plan is right

- **Mechanism matches the #18 design sketch verbatim** (sealed internal net stays the real enforcement; sidecar dual-homed; env opt-in convenience, routelessness for the malicious case).
- **ADR-0011 is the right number** (current top is `docs/adr/0010-build-isolation.md`).
- **The four hardening flags** (`cap_drop:ALL`, `read_only`, `no-new-privileges`, non-root `1000:1000`) are already the structural baseline applied uniformly by `internal/runcontract/compile.go:149-164` to every app service. The sidecar will inherit them automatically as a `ServiceSpec` — no special-casing needed, just declare and let the compiler harden it.
- **Reserved-name guard for `egress`** fits the existing pattern in `internal/runcontract/manifest.go:96-101` (currently `{db, seed}`).

## Things to change or tighten

**1. "Custom-proxy-registry precedent" is misleading.** There is no in-stack custom proxy precedent. The only proxy today is Traefik, which is **control-plane-owned, external, and attached post-boot** via `docker network connect` (`internal/runner/compose.go:379`). Drop that justification — it doesn't hold up. The real justifications are stronger anyway: (a) Go is already the platform language, (b) the auditability / "never an open relay" argument, (c) `host:port` allow-listing genuinely can't be done by tinyproxy.

**2. `plan.go` change is bigger than the bullet implies.** `ExecutionPlan.Network` is **a single value, not a slice** (`internal/runcontract/plan.go:9-11`) with an explicit comment "Network is the single isolated per-Session bridge." Adding `EgressNetwork *NetworkSpec` works but breaks the one-network mental model. Either own that in the ADR, or model it as `Networks []NetworkSpec` (cleaner long-term, slightly bigger blast radius today). Lean toward the optional pointer for minimality, but be explicit about it.

**3. Existing egress tests will regress.** `internal/runcontract/egress_test.go:36` pins that **every** service joins exactly `[netName]`. The egress sidecar joins two networks. That assertion needs to become: *app services still join only the internal net; egress sidecar (if present) joins both*. Add an explicit bullet to the test plan for "update existing pinned invariants" so the diff isn't a surprise during review.

**4. `NO_PROXY` value isn't specified.** It must include intra-stack service names (so app→db, ui→api don't pointlessly traverse the egress proxy), derived from the manifest's service list at compile time. Worth at least: `NO_PROXY=localhost,127.0.0.1,db,<service-names>`. This is a real correctness issue, not a polish thing.

**5. Allow-list contract under-specified.** State the env format the proxy reads (e.g. `EGRESS_ALLOWLIST=host1:443,host2:80`), the parse semantics, and that validation happens at compile time against `Egress []EgressRule` — the proxy itself is just a renderer of the validated list. Spec it once in the ADR so the proxy binary doesn't accidentally become the source of truth for what's valid.

**6. Owner-declared `HTTP_PROXY` collision.** If an Owner sets `HTTP_PROXY` in their manifest `Env`, what happens when egress is also declared? Platform-wins is the safe default; say so. (Likely also: reject `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` as Owner-set env names entirely — they're platform-reserved keys, mirroring the reserved-service-name pattern.)

**7. Bypass test missing from the integration list.** The whole point of routelessness is the malicious-app case. Add an explicit test: *an app that deliberately ignores `HTTP_PROXY` (e.g. direct dial to `1.1.1.1:443`) still has no route, even when an allow-list is declared.* That's the load-bearing claim of the design — pin it.

**8. Egress image provenance.** Today `opts.Images` is keyed per service and the runner fills it from `Project.Images` (`internal/runner/compose.go:277`). The egress image is **platform-owned**, not per-Project — it should be supplied like the Traefik container name is, via `runner.Options`, not via the Project's image map. Worth being explicit.

**9. Healthcheck + tiny resource caps for the sidecar.** Not in the plan. Even if app services don't `depends_on: egress (healthy)`, give it a `Healthcheck` and a small `Resources` cap (mirror `DBResources` pattern). Cheap and consistent with the rest of the stack.

**10. DNS resolution semantic.** Worth one line in the ADR: the proxy resolves allow-listed hostnames itself on the egress net; the app has no DNS path out. Forecloses a "rebind the allow-list host via DNS poisoning" question that a reviewer will otherwise raise.

## On the proxy binary itself

The "~120 lines" estimate is realistic if you stick to: parse env → build `map[string]map[int]bool` (or similar) → `http.Server` with two handlers (HTTP forward + CONNECT). A few specifics worth pinning in the binary:

- Validate the CONNECT target against the **post-parsed** `host:port` (not the raw line) — common allow-list bypasses come from sloppy parsing.
- Reject `Host:` rewriting tricks: enforce the allow-list against the actual destination, not the `Host` header.
- No HTTP/2 upstream; CONNECT is enough.
- Default-deny on any parse error.
- Log denials with destination, but never the request body or path — keep the audit trail useful without becoming a side channel.

## Recommendation

Proceed with the custom Go proxy, but before opening the PR, tighten the ADR draft on items 1, 4, 5, 6, 8, 10 above, and update the change list / test plan for items 2, 3, 7, 9. Most are one-line additions — the only material rework is deciding `EgressNetwork *NetworkSpec` vs `Networks []NetworkSpec` in `plan.go`.
