# ADR 0041: Task-agents are ephemeral instances of templates, not standing peers

**Status:** Proposed (2026-11-25)

## Context

ADR-0040 fixed the one agent that persists: a single orchestrator per user, always on. ADR-0037 point 1 already made the agent that runs a task ephemeral, spun up to run it and removed afterward through ADR-0006's explicit kill, never reaped automatically. What it left open is whether any agent other than the orchestrator carries a standing identity, or whether such agents are made and discarded per goal.

ADR-0039 named the ephemeral task-agents as a piece of the core to refine. This ADR fixes what every agent other than the per-user orchestrator is: an ephemeral instance of a template, holding no standing identity.

## Decision

1. **Every agent other than the per-user orchestrator is an ephemeral, goal-scoped task-agent.** It is a temporary agent (ADR-0006) launched with a clear goal to run a task (ADR-0037) and removed when the goal is met, changes, or is no longer needed, through ADR-0006's explicit kill and never auto-reaped. The only always-on agent is the orchestrator of ADR-0040.

2. **A task-agent is an instance of a template.** A template is a reusable shape, not a named agent. What persists in a template is its **memory** (ADR-0039 plane 2) and its **access**, the grants and tags it carries (ADR-0003, ADR-0005); what does not persist is its **identity**. The name of an instance and its backend, the model or CLI that runs it (ADR-0011), are disposable and may change between instantiations without the template losing its accumulated memory or access.

3. **A template is defined by its memory and access, not by a fixed agent.** Examples are a coding template and a review template. A template is instantiated on demand, per goal, and torn down after, by the per-user orchestrator (ADR-0040 point 4), which holds the grant to launch a task-agent. There is no roster of long-lived non-orchestrator agents: there are templates, and instances of them that come and go.

4. **No cross-cutting role is a standing agent.** Because the orchestrator (ADR-0040) is the only always-on agent, a concern that spans agents, such as governing what an agent's tool calls may reach, is carried by pao's own mechanisms, the approval gate (ADR-0004) and the connectivity plane (ADR-0039 plane 3), and not by a persistent agent dedicated to it.

The internals left open: the template's definition and where it is stored; exactly how a template's memory and access are expressed and bound to it (the tag-as-capability ADR that follows); and how many instances of one template may run at once. The connectivity plane that point 4 leans on is deferred to its own ADR (ADR-0039 plane 3).

## Consequences

The non-orchestrator agents stop being a fixed set of long-lived identities and become a small set of templates instantiated on demand, so what is maintained is a template's memory and access, not an agent's continuous life. A template's model or name can be swapped without losing what it has accumulated, since those live on the template and not the instance. Because no cross-cutting role is a standing agent, a concern like governing an agent's tool calls is a property of pao's mechanisms rather than of a watching agent. This **builds on ADR-0040** (its orchestrator is the one exception to the ephemerality here and the launcher of these instances), on **ADR-0006 and ADR-0037** (the temporary agent and the task it runs), and on **ADR-0039**; it supersedes none of them, since each is extended rather than contradicted.

The cost is a template abstraction that must be defined well enough that memory and access bind to it rather than to an instance. That is deliberate: binding memory and access to the template is what lets an instance be thrown away and remade, which is the property the whole ephemeral model depends on.
