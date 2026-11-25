# ADR 0041: Task-agents are ephemeral instances of templates, not standing peers

**Status:** Accepted (2026-11-25)

## Context

ADR-0040 fixed the one agent that persists from the node's start: a single orchestrator per user. ADR-0037 point 1 already made the agent that runs a task ephemeral, spun up to run it and removed afterward through ADR-0006's explicit kill, never reaped automatically. What it left open is whether a task-running agent carries a standing identity, or whether it is made and discarded per goal.

ADR-0039 named the ephemeral task-agents as a piece of the core to refine. This ADR fixes what the agent that runs a task is: an ephemeral instance of a template, holding no standing identity. It is about task-running agents only, and does not touch the other agents the existing ADRs keep.

## Decision

1. **An agent that pao dispatches a task to is an ephemeral, goal-scoped task-agent.** It is a temporary agent (ADR-0006) launched with a clear goal to run a task (ADR-0037) and removed when the goal is met, changes, or is no longer needed, through ADR-0006 point 3's explicit kill and never auto-reaped. This ADR concerns those task-running agents; it does not change the agents other ADRs keep that are not task-agents, among them the orchestrator (ADR-0040), a judge agent (ADR-0001 point 2, ADR-0004 point 5), and the governor (ADR-0034).

2. **A task-agent is an instance of a template.** A template extends ADR-0006 point 5's recipe, which bundles a profile (ADR-0015), a workdir source, and a tool set as data rather than code, by also carrying persistent **memory** (ADR-0039 plane 2) and the **access** its instances receive. A template is a reusable shape, not a named agent. An instance resolves its own single profile (ADR-0015 point 1) from the template, and the part chosen at that point, its name and the backend and model it runs on (ADR-0015 point 1, ADR-0011), is the disposable **identity** that may differ between instances; the rest of the template, its recipe, its memory, and its access, does not change with the instance, so the model can be swapped without the template losing what it has accumulated.

3. **Access is declared on the template and applied to each instance.** An instance still has its own presence in the access matrix (ADR-0005, ADR-0012), but the grants it receives are declared once on the template rather than hand-built on each ephemeral agent's row. This moves where access is expressed, from the per-agent row to the template, without loosening the default-deny of ADR-0005. The exact expression and binding of a template's access and memory is the subject of the tag-as-capability ADR that follows; this ADR fixes only that they live on the template, not on the disposable instance.

4. **Instantiation and teardown use the paths that already exist.** A template is instantiated per goal through the launch grant of ADR-0040 point 4, which keeps the operator's own spawn and pao's spawn at dispatch, and is torn down through ADR-0006 point 3's explicit kill, subject to the matrix (ADR-0005) and the gate (ADR-0004). This ADR adds no new spawn or teardown path and takes none away.

5. **A template's memory stays under the access model.** Memory carried on a template (ADR-0039 plane 2) is still subject to ADR-0012 point 2's default-deny: it grants no blanket access and never carries one subject's data into another's reach. Whether a template is per-user or shared with per-user-namespaced memory is deferred to the memory ADR; either way, template memory does not override the access model.

The internals left open: the template's definition and where it is stored; how exactly a template's access and memory are expressed and bound to it, and how an instance's matrix presence is derived from the template (the tag-as-capability ADR that follows); how many instances of one template may run at once; and whether a template is per-user or shared.

## Consequences

The task-running agents stop being long-lived identities and become instances of templates made and discarded per goal, so what is maintained is a template's memory and access rather than an agent's continuous life. A template's model or name can be swapped without losing what it has accumulated, since those live on the template and not the instance. Expressing access once on the template rather than on each ephemeral row is the change that makes an instance cheap to throw away and remake. This **builds on ADR-0040** (its orchestrator is the one that persists, and its point 4 grant launches these instances alongside the operator's own spawn and pao's spawn at dispatch), on **ADR-0006 and ADR-0037** (the temporary agent, its recipe, and the task it runs), on **ADR-0015** (the profile a recipe carries), and on **ADR-0039**; it supersedes none of them, since each is extended or cited rather than contradicted.

The cost is a template abstraction that must be drawn well enough that access and memory bind to it rather than to an instance, and that its relation to the existing recipe and profile stays a superset rather than a second, competing concept. That is deliberate: binding access and memory to the template is what lets an instance be thrown away and remade, which is the property the whole ephemeral model depends on.
