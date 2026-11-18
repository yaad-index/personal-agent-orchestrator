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
- **0023** — Agent I/O: channel bindings + the default agent (stdin/stdout/stderr per agent; stdout multiplexed, each channel a level against the profile map; default agent per channel, unprefixed, last in the routing precedence; `/pao` addressing by name or tag; channels as access-matrix resources shared by subjects; a single-conversation platform maps group topics to channels; extends 0003/0008/0009/0012/0013/0019)
- **0024** — pao as a proxy for upstream MCP servers (pao is an MCP client to upstream servers and re-exposes their tools on its own MCP endpoint; an agent connects to one MCP, pao's; each proxied tool is a matrix resource, shown and callable only per the caller's access, with no existence leak; every proxied call passes the matrix and gate before pao forwards it; extends 0004/0005/0012/0016)
- **0025** — An agent's context comes from its profile (by default a backend loads only its profile's working directory, pao's MCP configuration and pao's hook, nothing from the node user's environment; the backend adapter owns the isolation, a profile setting its options is refused; a profile inherits the user's context by named category; a backend that cannot isolate a category says so; contract test case; extends 0011/0015)
- **0026** — Operator commands from chat (`/pao` is pao's and never reaches an agent; a chat command is an existing operator operation under the same checks; steering and gate answers for an operator-routed subject within its access, administration stays at the operator surface; a command's answer goes only to its sender; help built from the command table and filtered by who asks; buttons are a typed command's shortcut, re-checked on press; addressing only as `/pao agent`, `/pao tag` or `/pao <name>`, no platform mention syntax; extends 0005/0009/0010/0022/0023)

## Planned (one ADR each, broad spine first then detail)
- **0017** — Mesh transport + the cross-node gaps (gRPC over mutual TLS, pinned node keys; extends 0013)
- **0018** — On-demand remote output (Node.Watch; master pulls a remote agent's rendered output until a shared channel exists; extends 0017)
- **0019** — Channel adapters + sender identity (compile-time adapters; (channel, sender id) binds to a human subject at registration; extends 0009/0011/0012)
- **0020** — Registration, adapter configuration, and the rejected-inbound record (operator-issued single-use invitation; explicit adapter registry; bounded coalesced record; extends 0019)
- **0021** — The composite message id (`<channel-id>-<native-id>`; numeric, never-reused channel ids; beside the pao id; extends 0010/0019)
- **0022** — Operator subjects (an operator-flagged human subject is routed as the operator; set only at the operator surface; extends 0020)

The audit log named as a foreseen role in ADR-0002 is not yet its own ADR; ADR-0019 is the first to require it, for recording a rejected channel inbound, and a full audit-log ADR would follow when more of the role is needed.
