---
Status: accepted
---

# Untrusted Owner builds run in a rootless-BuildKit sandbox; prod-grade isolation is a gate before public Owners

Build-from-source (#14) runs `docker build` on the host Docker daemon, so a Dockerfile's `RUN` steps execute on the control-plane VM. gVisor (ADR-0002) sandboxes the *runtime* Session, not the build; ADR-0007 permits build-time egress but says nothing about build *isolation*. While the only Project is the trusted in-repo fixture this is fine, but #19 wires **real Owner repos** — at which point an Owner's `RUN` is arbitrary code on the control-plane host at publish time. That is the same threat model as ADR-0002's runtime stance ("it's my own code" is not a boundary), one layer earlier: the build.

## Decision

Never run `docker build` on the host daemon for Owner input. Each build runs in a **throwaway, rootless BuildKit container** (`moby/buildkit:rootless`, daemonless via `buildctl-daemonless.sh`) that the platform launches as a locked-down `docker run`:

- **Rootless user namespace** maps the build's "root" to an unprivileged host UID — the actual isolation. Because of that remap, `RUN` steps that need root (`apk add`, package installs, multi-stage builds) still work *without* the outer container holding host privilege. We deliberately do **not** `--cap-drop ALL` / force `--user` on this path: that breaks the rootless setup, and the userns remap — not cap-drop — is the guarantee.
- **No Docker socket. No host mounts but the read-only build context.** An Owner `RUN` can reach neither the control-plane host filesystem nor other Projects/Sessions.
- **Resource caps** (`--memory`, `--cpus`, `--pids-limit`) plus a **wall-clock timeout**; on timeout the build container is force-removed, so a fork bomb / memory hog / infinite-loop `RUN` is bounded, not just the wall clock.
- **A dedicated build bridge network** with internet egress for dependencies (ADR-0007) but, as a separate L2 segment, **no route to the control plane or any Session/internal network**.
- **Output is an OCI image tar** streamed to stdout (`--output type=docker,dest=-`) and `docker load`ed into the local store under the same tag the host build produced — so the `internal/builder` cache contract (cache-by-version, invalidation, LRU eviction) and the Runner are unchanged.

Three things this decision locks in — so "pragmatic" does not quietly become "unbounded":

1. **The gate is adversarial boundary tests, not happy-path.** `internal/builder` integration tests prove the isolation holds: a malicious `RUN` cannot reach the control plane / a Session over the network, a runaway `RUN` is killed by the caps/timeout, and the lockdown argv carries no Docker socket and no writable host mount. These tests are what #19 depends on and they run in CI.
2. **Isolation level is tied to Owner trust as a named threshold.** Rootless BuildKit under `runc` (shared host kernel) is sufficient for #19's **trusted-ish Owners** — the maintainer and invited friends. Opening builds to **anonymous / public Owners is gated on a prod-grade upgrade**: gVisor-wrapped builds and/or a dedicated build VM (the ADR-0009 Azure host), giving a second isolation boundary beyond the shared kernel. This is a *required gate*, not "prod hardening someday."
3. **The residual is named honestly.** Rootless BuildKit under `runc` shares the host kernel, so a determined kernel-escape *during a build* remains possible until the prod-grade isolation lands; it also requires `seccomp=unconfined` + `apparmor=unconfined` (to set up its userns + overlay snapshotter) and `systempaths=unconfined` (so its nested `RUN` steps can mount their own `/proc`, which Docker's default proc masking otherwise refuses), widening the build container's own surface (mitigated, not removed, by the userns remap — these unmask the *build sandbox's* `/proc` and profiles, not the host's). Build egress is currently the open internet, not a registry allow-list, and the build bridge can still reach host-published ports via the docker gateway. The prod-grade hardening belongs on the Linux **prod** host (ADR-0009), not WSL2 — it is deferred to prod hardening **because the second boundary belongs there, not because it doesn't matter**.

## Considered options

- **Rootless BuildKit in a throwaway container** *(chosen)* — daemonless, userns-isolated, keeps real `RUN`-as-root builds working, exports a loadable tar. The userns remap is a real host-privilege boundary on the existing single VM, with no new infrastructure.
- **kaniko** — the most turnkey daemonless builder, but to keep real builds working it must run effectively root-in-container (it cannot honor non-root + `--cap-drop ALL`), and its own guidance is to run it under gVisor — which dev/WSL2 lacks. Weaker pragmatic isolation than the userns remap for the same operational cost.
- **buildah / rootless `buildctl` daemon** — comparable isolation to chosen, more setup (subuid/subgid, fuse-overlayfs lifecycle) for no extra safety here.
- **A dedicated build VM / gVisor-wrapped builds now** — the prod-grade target (lock-in #2). Deferred: it belongs on the ADR-0009 prod host, and the userns sandbox already meets #19's trust threshold on the single dev/MVP VM.

## Consequences

- The build joins the isolation model as a fourth tracked surface alongside ADR-0002's three legs (resource caps, escape, egress): an untrusted Owner `RUN` is sandboxed at *publish* time, not just at *runtime*.
- A first build pulls the BuildKit image and creates the build network; cached builds skip both. Build output round-trips through a tar + `docker load` rather than landing directly in the store — a small, contained cost for keeping the cache contract intact.
- `RUNTIME=runsc` (gVisor) still governs only the runtime Session, not the build — wrapping builds in gVisor is the lock-in #2 upgrade, on the swap point this ADR establishes.
- Sandbox parameters are config-driven (`BUILD_SANDBOX_IMAGE`, `BUILD_NETWORK`, `BUILD_MEMORY_MB`, `BUILD_CPUS`, `BUILD_PIDS_LIMIT`, `BUILD_TIMEOUT`), mirroring how `RUNTIME` selects the runtime — but there is deliberately **no** switch to disable the sandbox and build on the host daemon, which would reopen exactly this hole.
