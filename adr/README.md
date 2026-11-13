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

## Planned (one ADR each, broad spine first then detail)
- **0014** — Budget + outbound notifications (one notifier; gate exempt; budget off subscription usage)
- Budget + outbound notifications
- Audit log
