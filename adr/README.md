# pao ADRs — index

Architecture Decision Records for **pao** (personal-agent-orchestrator). Convention: ADR-0000. Broad decisions first, detail added in successive ADRs. Immutable once Accepted; a later ADR supersedes an earlier one.

A superseded ADR is never edited or deleted: it keeps a **Superseded-by** header pointing to its replacement and stays in place, out of circulation. Supersession is all-or-nothing, never partial; the successor states which parts it keeps and which it changes.

## Accepted
- **0000** — Record architecture decisions (meta)
- **0001** — Node mesh that routes input to agents (the spine)
- **0002** — Stack and conventions
- **0003** — Routing (agent name + tags)
- **0004** — The approval gate (risk-level policy; fail-closed; judge for fuzzy cases)
- **0005** — Access model + management UI (localhost-default)
- **0006** — Temporary agents (ephemeral, first-class, explicit teardown, recipes)
- **0007** — Control plane (lifecycle, context watch, liveness)
- **0008** — Mirror + verbosity levels + retention
- **0009** — Communication channels (human↔pao)
- **0010** — The message model (ids/quote; reactions target an id; buttons over reactions)
- **0011** — Pluggable backends (profile declares per-backend specifics; contract test; startup)
- **0012** — Multiple humans + tag-scoped access (extends 0005)
- **0013** — Node transport + failover (coordination/stream split; manual promotion)
- **0014** — Budget + outbound notifications (one notifier; gate exempt; per-account budget)
- **0015** — Profile inheritance + the account field (extends 0011)
- **0016** — pao as an MCP server (pao operations as MCP tools; matrix- and gate-bound)

## Planned (one ADR each, broad spine first then detail)
- **0017** — Mesh transport + the cross-node gaps (gRPC over mutual TLS, pinned node keys; extends 0013)
- **0018** — On-demand remote output (Node.Watch; master pulls a remote agent's rendered output until a shared channel exists; extends 0017)
- **0019** — Channel adapters + sender identity (compile-time adapters; (channel, sender id) binds to a human subject at registration; extends 0009/0011/0012)

The audit log named as a foreseen role in ADR-0002 is not yet its own ADR; ADR-0019 is the first to require it, for recording a rejected channel inbound, and a full audit-log ADR would follow when more of the role is needed.
