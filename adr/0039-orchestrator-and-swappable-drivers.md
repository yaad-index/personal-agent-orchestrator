# ADR 0039: pao is the orchestrator; every external capability is a swappable driver

**Status:** Proposed (2026-11-25)

## Context

ADRs 0000–0038 built pao's orchestration spine: the node mesh and routing, the access matrix and gate, channels, operator commands, tasks, budget. In building it, pao also reached directly for several external capabilities, and each of those is a field that moves on its own and matures faster than pao can: model runtimes and agent CLIs, memory and retrieval, MCP/tool proxying, credential handling, and the material a task works on. Binding the core to any one implementation of these makes pao reinvent wheels it should borrow, and couples its lifetime to a choice that will age.

ADR-0011 already made the model/agent backend pluggable, with a profile declaring per-backend specifics and a contract test. This ADR generalises that stance into the architecture's organising principle, so the boundary is the same for every external capability, not only the model backend.

## Decision

pao is the **orchestrator**, and it owns the orchestration core and nothing beyond it: agents and their lifecycle, tasks, tags, channels, the access matrix and the gate, launch, and the data round-trip. Every **external capability** the core depends on is reached only through a **narrow driver interface**, never a concrete implementation. The core knows the interface and its guarantees; it holds no knowledge of any driver's internals.

The external capabilities, each its own driver plane, are:

1. **Agent runtime** — the model/agent backend that runs an agent (generalises ADR-0011).
2. **Memory** — storing and retrieving what an agent or template should remember.
3. **Connectivity** — the MCP/tool plane pao already proxies (ADR-0024) and the secret/credential plane that fronts an agent's outbound tool calls.
4. **Data source** — the working material a task runs on (a repository, a folder within one, or an archive), provisioned by the orchestrator.
5. **Secrets** — resolving a reference to a real credential at run time, so the value never lives in a task file or on a channel (ADR-0037 already requires this).

Each plane follows the same rule, which is the principle behind the whole design: **do not reinvent the wheel; put an interface between pao and the thing.** A plane ships a **simple first implementation** that pao can run with no external dependency, plus a **contract test** that any implementation must pass (as ADR-0011 established for the backend). A mature external system may then replace that first implementation **behind the same interface**, and this ADR names no such system as mandatory; the choice of backend for a plane is configuration, decided in that plane's own ADR, never baked into the core. A driver that cannot meet a guarantee **declares so** rather than failing silently, the same stance ADRs 0025 and 0033 take for a backend that cannot isolate context.

The redesign is carried out in the ADRs that follow this one, each routed and accepted before the code it governs, and they fall into two kinds. Some refine the orchestration **core** itself, the part line 13 above names as pao's own: the per-user orchestrator and the ephemeral task-agents, the tag as the capability primitive, the channel and preprocessing model, the UI surface, and pao-native budgeting. The rest draw a **driver plane** named here: the data-source provisioning and its versioned round-trip, the memory driver, and the credential-injection connectivity plane. Of the five planes, two need no new ADR and are cited above: the agent-runtime plane is ADR-0011, generalised by this one, and the Secrets plane is already governed by ADR-0037.

## Consequences

Each external capability becomes independently replaceable and independently testable, and pao's core stops carrying the risk of any one external choice aging. The contract test per plane is what keeps a swap honest. This **builds on ADR-0011**, widening its per-backend plug to every external plane; 0011's backend contract test and its "a backend that cannot do X says so" rule are kept and generalised, and 0011 is not superseded, since its specific subject, the agent runtime, is one of these planes.

The cost is up front: five interfaces must be drawn well enough that a second implementation can slot in later, and an interface drawn too narrow or too wide will churn before it settles. The mitigation is the same simple-first discipline the principle demands, prove each plane with the smallest implementation that works before a mature backend is considered, so the interface is shaped by a running thing rather than by a product's feature list.
