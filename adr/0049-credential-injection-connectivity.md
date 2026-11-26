# ADR 0049: Credential injection, the egress proxy that fills a task's secret

**Status:** Proposed (2026-11-26)

Draws driver plane 3 (Connectivity) of ADR-0039, the credential-injection half, consuming the Secrets interface of ADR-0037. Builds on ADR-0039 (the plane and its driver discipline), ADR-0037 (a task's secret reference and its per-run resolution), ADR-0024 (the MCP/tool half of connectivity), ADR-0025 point 4 (a guarantee pao cannot keep is never reported as kept), ADR-0012 (default-deny), ADR-0006 and ADR-0011 (the temporary agent and runtime it isolates), ADR-0015 (the profile), and ADR-0004 and ADR-0005 (the gate and matrix).

## Context

ADR-0039 named plane 3, Connectivity, as the MCP/tool plane pao already proxies (ADR-0024) together with **the point at which an agent's outbound tool calls are fronted with an injected credential**, and placed credential injection **across planes 3 and 5**: Secrets (ADR-0037) resolves a reference to a value, and Connectivity is where that value is put into an outbound call. The credential-injection driver is therefore a Connectivity driver that consumes the Secrets interface, and the two stay distinct.

ADR-0037 point 3 already resolves a task's secret to a scoped environment for the one run, keeps the value out of the task file, and keeps it off every channel, but once the agent reads that environment the value is in the agent's process and its transcript, which is the open path ADR-0037 point 3 flags. This ADR draws the connectivity half that closes it for an injected credential: the agent calls with a **placeholder**, a proxy fills the **real** value into the outbound request at the boundary, and an enforced egress makes that proxy the only way out.

A spike on 2026-11-25 proved the mechanism viable (the fake-to-real header swap by host, and egress that fails closed). This ADR takes the mechanism, not the spike's tooling.

## Decision

1. **The credential-injection driver is a Connectivity driver (ADR-0039 plane 3) that consumes the Secrets interface (ADR-0037), and it is pao's own code.** It is written in pao's own language against the standard library (TLS, x509, a reverse proxy), a simple-first implementation with no external dependency, as ADR-0039 requires of a first plane implementation. The 2026-11-25 spike used mitmproxy only to prove the mechanism; the production proxy is in-core, not pao supervising an external tool.

2. **The agent calls with a placeholder; the proxy fills the real secret by host, and never logs the filled value.** An agent's outbound call carries a **placeholder** credential. The proxy substitutes the **real** value, resolved for that run under ADR-0037 point 3, onto the outbound request, matched by host. The real value is put into the call only at the boundary, so the agent process never holds it: the placeholder is what lives in the agent's environment and its transcript, not the secret. The proxy **never records the filled value** in any log or capture; a mode that would record it is refused, not run.

3. **Enforced egress is the boundary; the proxy is a boundary only with it.** The agent runs in its own network namespace with loopback only and a single bridge to the proxy's socket, so every outbound call goes through the proxy or fails closed. A client that ignores proxy environment variables cannot bypass it, it simply fails to resolve. Without this enforcement the proxy is not a boundary at all, so this ADR requires the enforcement and not merely the proxy.

4. **Egress is default-deny, in three classes (ADR-0012 point 2).** All egress is denied unless the proxy allows it, as one of: **inject**, intercept the request and fill a secret, for the hosts a **task's** secret references name; **pass**, a CONNECT tunnel with no interception, for pinned hosts a **profile** names (ADR-0015), such as the agent backend's own API and pinned clients; or otherwise **403**. The task names its inject entries, the profile names its pass hosts, and nothing else leaves.

5. **Where the host cannot enforce isolation, a task needing an injected secret is refused, never run unenforced.** The enforcement needs an unprivileged user namespace, which some hosts disable by default (Ubuntu 24.04 among them). Where it is unavailable, the driver **declares it cannot enforce** (ADR-0039's declare-so stance; ADR-0025 point 4, a guarantee pao cannot keep is never reported as kept) and pao **refuses** a task that needs an injected secret on that host rather than running it unenforced. A task with no injected secret is unaffected.

6. **The isolation level is stated, and it is rootless.** Enforcement is the network namespace (egress), the mount namespace (the secret store masked from the agent), and the pid namespace; no separate proxy uid is required. Stating the level makes explicit what the boundary covers, an agent cannot reach the secret store or bypass egress, and what it does not, so a later reader does not assume more.

7. **An injected-secret run is a secret run, and its rules hold.** The value is resolved only at a run whose task references it, for that run alone (ADR-0037 point 3); no memory is written from it (ADR-0043 point 4) and no data round-trip fires (ADR-0042 point 5). Because the agent holds only the placeholder, ADR-0037 point 3's transcript concern does not arise for an **injected** secret; it still arises for a secret the older path materializes directly into the agent's own environment, which this ADR does not remove.

## What this ADR deliberately does NOT decide

- The exact placeholder format and the per-host match rules for an inject entry.
- What a host that cannot enforce is offered beyond refusal (a reduced, no-inject mode is left open), and how that refusal is surfaced to the operator.
- Whether the pass class pins a host by certificate, and the proxy's full logging stance beyond never recording a filled value.
- Cross-node egress, which no accepted ADR provides today (ADR-0013 point 4 keeps per-agent data node-local).
- The MCP/tool half of plane 3 (ADR-0024), which this ADR does not reopen.

## Consequences

An agent never holds an injected credential: it calls with a placeholder, the proxy fills the real value at the boundary and never logs it, and the agent's process and transcript carry only the placeholder, which closes ADR-0037 point 3's open path for an injected secret. Egress is default-deny and the proxy is the only way out, enforced by namespaces so a client that ignores proxy settings fails closed rather than leaking; and where a host cannot provide that enforcement, a task needing an injected secret is refused rather than run unsafe, so the plane never claims a boundary it is not keeping. This **builds on ADR-0039** (plane 3 and its discipline), **ADR-0037** (the secret reference and per-run resolution whose transcript path this closes for injected secrets), **ADR-0024** (the MCP/tool half of connectivity), **ADR-0025 point 4** (never report an unkept guarantee), **ADR-0012** (default-deny), **ADR-0006 and ADR-0011** (the temporary agent and runtime), **ADR-0015** (the profile naming pass hosts), **ADR-0004 and ADR-0005** (the gate and matrix), and **ADR-0042 and ADR-0043** (the secret-run rules); it supersedes nothing.
