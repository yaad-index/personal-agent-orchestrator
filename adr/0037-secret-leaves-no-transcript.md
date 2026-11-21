# ADR 0037: A task's secret leaves nothing in the harness's record

**Status:** Accepted (2026-11-21)

Supersedes ADR-0036 (tasks, the unit of work pao dispatches). Restates its points 1, 2 and 4 to 6 and its deferred list unchanged, and carries over its consequences. Changes point 3, which now also removes the backend's transcript of a run that had a secret. Builds on ADR-0035, ADR-0006, ADR-0003, ADR-0016, ADR-0015 point 4 and ADR-0007, as ADR-0036 does.

## Context

ADR-0036 point 3 gives a task's secret to the one run that needs it: the value is materialized for that run alone and removed when the run ends, including on a crash or a kill. Its deferred list leaves open what the agent itself does with the value: copying it into its output, and from there into ADR-0006 point 4's harvest.

A test run of a task with a secret, on a node with only the local channel and a fake value, found a third place the value lands that is neither of those. The agent read the secret's file, as it must to use it. The backend, the coding-agent harness, keeps its own transcript of every session, under the node user's home and outside pao's state directory and the agent's working directory, and that transcript holds the tool's result: the value. pao's run-scoped file was removed at the kill as designed, and the value was in nothing under pao's state directory. But the transcript stayed on disk after the run, with the value in it.

That is not the agent copying the value somewhere; the agent did only what using the secret requires. It is the harness recording a tool result automatically, on every run that reads a secret. Point 3's "for that run alone" is what it breaks, so it is pao's to clean. Because the deferred list leaves the agent's own copying open, a reader could take the transcript to be part of what is deferred. This ADR says it is not.

## Decision

ADR-0036's decision, restated whole so this record stands alone. Only point 3 changes, by the part marked **New**.

1. **The task is the unit of work pao dispatches to an agent.** A task is a unit of dispatched work, not every pao operation: an operator's `/pao` command (ADR-0026) and a governor translation (ADR-0034) are not tasks. Apart from a small number of always-on agents that persist to answer and to pick tasks up (the coordinator and perhaps one more), an agent that runs a task is ephemeral, spun up to run it and removed afterward through ADR-0006's explicit kill, never reaped automatically. A task is created by an operator or by another agent through pao's MCP surface (ADR-0016), and pao owns dispatch.

2. **A task carries its definition, its how, its attachments, and an owner.** A task holds a description of what to do and its definition-of-done; the *how*, which skill, tool or template it uses, carried inside the definition; tags; and the attachments an agent needs to run it: tools, scripts, and a reference to an optional ephemeral secret. It names an owner, a user or an agent. An attachment widens nothing: a tool or script a task carries stays bound by the running agent's access matrix (ADR-0005) and the gate (ADR-0004), so a task hands an agent no capability its own grants do not already allow, as ADR-0016 point 4 holds for pao's own surface. The format of the *how*, the template, is deferred.

3. **Tags and budget decide where a task runs.** pao dispatches a task by its tags: it runs on a running agent that carries those tags, or pao spawns a one-off agent carrying them (ADR-0003, ADR-0006). Among eligible agents pao picks one whose account has budget headroom (ADR-0035); an unmetered account (ADR-0035 point 2) always qualifies, and a task waits rather than run against a metered account with no headroom or one whose figure cannot be read (ADR-0035 point 3). A secret the task references is resolved at run from the orchestrator config that holds it, extending ADR-0015 point 4's split (the credential lives in the orchestrator config, not the agent) to a task's secret, materialized as a scoped temporary environment for that run alone and removed when the run ends including on a crash or a kill; the task file holds only the reference, never the secret's value, and the value never crosses a channel. **New: the harness's record of that run goes with it.** The backend keeps its own record of a session, its transcript, and that record holds what the run's tools returned, the secret's value among them once the agent has read it. When the one-off that ran a task with a secret is killed, pao removes that agent's transcript after the harvest, which copies the working directory and not the transcript, and confirms it is gone. A removal that fails, or a transcript pao cannot find where its backend says it is, is reported to the operator, never passed over in silence. What the agent itself copies elsewhere, into its output or its working directory, stays deferred, as ADR-0036's deferred list leaves it.

4. **The running agent reports on the task; pao tracks its state and does not judge done.** The agent working a task comments on it, changes its status, and logs to it, and pao tracks the task's state from that. pao does not itself decide a task is done; the definition-of-done is reported met by the agent, because the task is the agent's own assignment. This is distinct from health and liveness (ADR-0007), which pao still reads independently of the agent, so a task whose agent has stalled is flagged (ADR-0007 point 5) rather than left waiting on a report that will not come.

5. **Tasks relate to each other.** A task can be created from another and is tied to it: a review task created from the task it reviews is linked to that parent. pao keeps these relations, so a task's origin and what it spawned are known.

6. **Tasks are file-based, and the type set and the folders stay open.** A task is a file, its exact format a later detail. pao keeps tasks in folders, a template folder, a recurring folder, an archive, and this ADR does not fix that set: a folder is added when it is needed. The kinds of task, likewise, are examples not a closed list: a one-off that completes, a recurring one that re-queues itself, one that waits on the board until its account has budget. Types are added or removed without a new ADR. What this ADR fixes is the generic task and the dispatch-and-report model, never the catalogue of types or folders.

## What this ADR deliberately does NOT decide

The set of task types and lifecycle shapes (open, grown without an ADR). The storage folders (open). The task file format and the how-template's format. The dispatch scheduling beyond tags-and-headroom: the order among waiting tasks, polling versus event, and concurrency. How the always-on agents are chosen. How a recurring task's schedule is expressed (it replaces the external cron). How the orchestrator config that a task's secret reference resolves against holds and resolves it (the ADR-0015 point 4 split this extends). Keeping a run's secret value out of ADR-0006 point 4's harvest store, which other agents may read, should the value surface in the agent's output: the reference model keeps the value out of the task file and off channels, but does not by itself close that path. What pao does with a task whose agent has stalled, beyond flagging it (ADR-0007 point 5 flags a stalled agent, it does not auto-kill): whether the task is re-dispatched, held, or surfaced is deferred.

## Consequences

ADR-0036's consequences carry over unchanged. In addition:

- At the end of a run, a task's secret leaves both places the test run found it on the node user's disk: pao's run-scoped file, and now the harness's transcript of the run.
- The transcript of a run with a secret is not kept for later reading. What the run produced that matters is in the task's log and the harvest of its working directory.
- A transcript that cannot be removed, or found, is a visible failure to the operator, not a value left behind in silence. If the backend changes where it keeps transcripts, that shows at the first run with a secret.
- ADR-0036 is superseded in full. Its points 1, 2 and 4 to 6 and its deferred list are restated above unchanged, and its consequences carry over. Only point 3 changes, with the same text and the new sentences about the transcript. What the agent copies on its own, into its output or working directory, is still the deferred case.
