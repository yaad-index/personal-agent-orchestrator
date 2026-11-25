# ADR 0040: One always-on orchestrator per user, backed by that user's vault

**Status:** Proposed (2026-11-25)

## Context

ADR-0001 made pao a per-machine node mesh that routes input to agents, and the task ADRs (ADR-0036, superseded by ADR-0037) drew the line between the agents that persist and the agents that do not: "a small number of always-on agents that persist to answer and to pick tasks up (the coordinator and perhaps one more)", with everything else ephemeral. ADR-0037 deferred **how the always-on agents are chosen**.

ADR-0039 made pao the orchestrator and named the per-user orchestrator as the first piece of the core to refine. This ADR settles ADR-0037's deferral for the always-on side: who is always-on, what that agent is for, and what durably backs it. The ephemeral task-agents are the next ADR's subject.

## Decision

1. **The always-on set is exactly one orchestrator per user.** For each registered user there is one persistent agent, bound to that user and to nothing else. This narrows ADR-0037's "a small number... perhaps one more" to a precise rule: the coordinator is per-user, not a free-floating pool, and the count of always-on agents is the count of registered users. Binding is to the user, not to the machine ADR-0001 runs it on.

2. **The orchestrator orchestrates; it does not do the work.** Its job is to answer its user, to dispatch and track tasks, and to decide; the work itself runs in ephemeral, goal-scoped task-agents (the next ADR). An orchestrator that never runs long work stays reachable to its user, which is the property that makes a single always-on agent per user enough.

3. **The orchestrator is backed by that user's vault.** The vault is a git repository of Markdown files the orchestrator reads and writes, and it holds that user's tasks, follow-ups, and state, so the orchestrator's working memory is durable and portable rather than living in a process. **Registering a user requires providing that vault repository**; a user without one cannot be registered. The vault is the orchestrator's own persistent backing and is distinct from the data source a task runs on (ADR-0039 plane 4): a **task-agent is not vault-backed**, it is provisioned a source per task.

4. **Spawning an agent is the orchestrator's exclusive capability.** Creating a task-agent is itself a task that only a per-user orchestrator may perform, issued through pao's MCP surface (ADR-0016) and checked under the access matrix (ADR-0005) like any other operation. No task-agent spawns another; the authority to launch work stays with the one agent accountable to the user.

The internals left open: the vault's layout and the files the orchestrator keeps in it; how a user and their vault are registered at the management surface (ADR-0005); whether one node may host orchestrators for several users or one each; how an orchestrator is brought up and recovered after a restart from its vault; and whether a user may ever hold more than one orchestrator. These settle in the ADRs that follow or in code, not here.

## Consequences

Each user has exactly one agent accountable for their work, with a durable, portable, git-backed record of it, and the collapse of v1's standing multi-agent fleet into one always-on agent per user plus ephemeral task-agents (the next ADR) follows from this point and ADR-0039 together. Because the orchestrator only orchestrates, one per user is cheap enough to keep always on, and keeping long work out of it is what preserves its reachability. This **builds on ADR-0037** by settling its deferred "how the always-on agents are chosen" and on ADR-0039 as the first core-refining ADR of the redesign; it supersedes neither, since ADR-0037's subject, the task and its dispatch, is unchanged, and the task-agent side it mentions is drawn in full by the next ADR.

The cost is a standing agent per registered user rather than a shared pool, and a hard registration prerequisite: no vault, no user. Both are deliberate. The per-user agent is what gives each user a single accountable orchestrator, and the vault requirement is what makes that orchestrator's state outlive any one process or machine.
