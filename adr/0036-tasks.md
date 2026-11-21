# ADR 0036: Tasks — the unit of work pao dispatches

**Status:** Accepted (2026-11-21)

**Superseded-by:** ADR-0037 (a task's secret leaves nothing in the harness's record)

Builds on ADR-0035 (per-account budget, for dispatch headroom), ADR-0006 (temporary agents), ADR-0003 (agent name and tags), ADR-0016 (pao as an MCP server), ADR-0015 point 4 (the credential lives in the orchestrator config, not the agent), ADR-0007 (the control plane). Relates to ADR-0026 (operator commands) and ADR-0034 (the governor), which are not tasks.

## Context

Recurring work today is scattered across external cron jobs that each fire a one-off prompt, and dispatch, choosing which agent runs what, is done by hand by the always-on coordinator. There is no first-class unit that carries what to do, how to do it, who owns it, and what done means, that pao can route to an agent with headroom and then track. This ADR makes the task that unit. It fixes the **generic** shape of a task and how pao dispatches and tracks it, and deliberately leaves the set of task types, the storage folders, and the how-template's format open to grow without another ADR.

## Decision

1. **The task is the unit of work pao dispatches to an agent.** A task is a unit of dispatched work, not every pao operation: an operator's `/pao` command (ADR-0026) and a governor translation (ADR-0034) are not tasks. Apart from a small number of always-on agents that persist to answer and to pick tasks up (the coordinator and perhaps one more), an agent that runs a task is ephemeral, spun up to run it and removed afterward through ADR-0006's explicit kill, never reaped automatically. A task is created by an operator or by another agent through pao's MCP surface (ADR-0016), and pao owns dispatch.

2. **A task carries its definition, its how, its attachments, and an owner.** A task holds a description of what to do and its definition-of-done; the *how*, which skill, tool or template it uses, carried inside the definition; tags; and the attachments an agent needs to run it: tools, scripts, and a reference to an optional ephemeral secret. It names an owner, a user or an agent. An attachment widens nothing: a tool or script a task carries stays bound by the running agent's access matrix (ADR-0005) and the gate (ADR-0004), so a task hands an agent no capability its own grants do not already allow, as ADR-0016 point 4 holds for pao's own surface. The format of the *how*, the template, is deferred.

3. **Tags and budget decide where a task runs.** pao dispatches a task by its tags: it runs on a running agent that carries those tags, or pao spawns a one-off agent carrying them (ADR-0003, ADR-0006). Among eligible agents pao picks one whose account has budget headroom (ADR-0035); an unmetered account (ADR-0035 point 2) always qualifies, and a task waits rather than run against a metered account with no headroom or one whose figure cannot be read (ADR-0035 point 3). A secret the task references is resolved at run from the orchestrator config that holds it, extending ADR-0015 point 4's split (the credential lives in the orchestrator config, not the agent) to a task's secret, materialized as a scoped temporary environment for that run alone and removed when the run ends including on a crash or a kill; the task file holds only the reference, never the secret's value, and the value never crosses a channel.

4. **The running agent reports on the task; pao tracks its state and does not judge done.** The agent working a task comments on it, changes its status, and logs to it, and pao tracks the task's state from that. pao does not itself decide a task is done; the definition-of-done is reported met by the agent, because the task is the agent's own assignment. This is distinct from health and liveness (ADR-0007), which pao still reads independently of the agent, so a task whose agent has stalled is flagged (ADR-0007 point 5) rather than left waiting on a report that will not come.

5. **Tasks relate to each other.** A task can be created from another and is tied to it: a review task created from the task it reviews is linked to that parent. pao keeps these relations, so a task's origin and what it spawned are known.

6. **Tasks are file-based, and the type set and the folders stay open.** A task is a file, its exact format a later detail. pao keeps tasks in folders, a template folder, a recurring folder, an archive, and this ADR does not fix that set: a folder is added when it is needed. The kinds of task, likewise, are examples not a closed list: a one-off that completes, a recurring one that re-queues itself, one that waits on the board until its account has budget. Types are added or removed without a new ADR. What this ADR fixes is the generic task and the dispatch-and-report model, never the catalogue of types or folders.

## What this ADR deliberately does NOT decide

The set of task types and lifecycle shapes (open, grown without an ADR). The storage folders (open). The task file format and the how-template's format. The dispatch scheduling beyond tags-and-headroom: the order among waiting tasks, polling versus event, and concurrency. How the always-on agents are chosen. How a recurring task's schedule is expressed (it replaces the external cron). How the orchestrator config that a task's secret reference resolves against holds and resolves it (the ADR-0015 point 4 split this extends). Keeping a run's secret value out of ADR-0006 point 4's harvest store, which other agents may read, should the value surface in the agent's output: the reference model keeps the value out of the task file and off channels, but does not by itself close that path. What pao does with a task whose agent has stalled, beyond flagging it (ADR-0007 point 5 flags a stalled agent, it does not auto-kill): whether the task is re-dispatched, held, or surfaced is deferred.

## Consequences

Recurring work and one-offs become one first-class thing pao routes and tracks, in place of scattered cron prompts and dispatch by hand. Because dispatch is tags plus budget, work only lands on an agent whose account has headroom, and a task with nowhere to run waits instead of failing. Because the agent reports on the task and pao reads liveness separately, "where is this" is answerable and a stalled task does not hang silently. Keeping the types and folders open lets the model grow without re-opening this ADR. This is the step that moves the fleet onto pao's dispatch rather than the coordinator's hands.
