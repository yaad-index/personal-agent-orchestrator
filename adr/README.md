# pao ADRs — index

Architecture Decision Records for **pao** (personal-agent-orchestrator). Convention: ADR-0000. Broad decisions first, detail added in successive ADRs. Immutable once Accepted; a later ADR supersedes an earlier one.

## Accepted
- **0000** — Record architecture decisions (meta)
- **0001** — Node mesh that routes input to agents (the spine)
- **0002** — Stack and conventions
- **0003** — Routing (agent name + tags)
- **0004** — The approval gate (risk-level policy; fail-closed; judge for fuzzy cases)
- **0005** — Access model + management UI (localhost-default)
- **0006** — Temporary agents (ephemeral, first-class, explicit teardown, recipes)
- **0007** — Control plane (lifecycle, context watch, liveness)

## Planned (one ADR each, broad spine first then detail)
- **0008** — Mirror + verbosity levels + retention
- Communication channels (human↔pao)
- Message model (ids/quote; buttons over reactions)
- Pluggable backends (profile; transcript contract test; startup)
- Node transport + failover
- Budget + outbound notifications
- Audit log
