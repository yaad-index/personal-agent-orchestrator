# pao ADRs — index

Architecture Decision Records for **pao** (personal-agent-orchestrator). Convention: ADR-0000. Broad decisions first, detail added in successive ADRs. Immutable once Accepted; a later ADR supersedes an earlier one.

## Accepted
- **0000** — Record architecture decisions (meta)
- **0001** — Node mesh that routes input to agents (the spine)
- **0002** — Stack and conventions

## Planned (one ADR each, broad spine first then detail)
- **0003** — Routing (agent name + tags)
- The approval gate (by retractability; fail-closed; judge for fuzzy cases)
- Access model + management UI (localhost-default)
- Temporary agents (ephemeral, first-class, explicit teardown, recipes)
- Control plane (lifecycle, context watch, liveness)
- Mirror + verbosity levels + retention
- Communication channels (human↔pao)
- Message model (ids/quote; buttons over reactions)
- Pluggable backends (profile; transcript contract test; startup)
- Node transport + failover
- Budget + outbound notifications
- Audit log
