# ADR 0037: A task's secret leaves nothing in the harness's record

**Status:** Proposed

Supersedes ADR-0036 (tasks, the unit of work pao dispatches). Keeps its points 1, 2 and 4 to 6, its deferred list and its consequences unchanged. Changes point 3, which now also removes the backend's transcript of a run that had a secret. Builds on ADR-0035, ADR-0006, ADR-0003, ADR-0016, ADR-0015 point 4 and ADR-0007, as ADR-0036 does.

## Context

ADR-0036 point 3 gives a task's secret to the one run that needs it: the value is materialized for that run alone and removed when the run ends, including on a crash or a kill. Its deferred list leaves open what the agent itself does with the value: copying it into its output, and from there into ADR-0006 point 4's harvest.

A test run of a task with a secret, on a node with only the local channel and a fake value, found a third place the value lands that is neither of those. The agent read the secret's file, as it must to use it. The backend, the coding-agent harness, keeps its own transcript of every session, under the node user's home and outside pao's state directory and the agent's working directory, and that transcript holds the tool's result: the value. pao's run-scoped file was removed at the kill as designed, and the value was in nothing under pao's state directory. But the transcript stayed on disk after the run, with the value in it.

That is not the agent copying the value somewhere; the agent did only what using the secret requires. It is the harness recording a tool result automatically, on every run that reads a secret. Point 3's "for that run alone" is what it breaks, so it is pao's to clean. Because the deferred list leaves the agent's own copying open, a reader could take the transcript to be part of what is deferred. This ADR says it is not.

## Decision

3. **Tags and budget decide where a task runs.** pao dispatches a task by its tags: it runs on a running agent that carries those tags, or pao spawns a one-off agent carrying them (ADR-0003, ADR-0006). Among eligible agents pao picks one whose account has budget headroom (ADR-0035); an unmetered account (ADR-0035 point 2) always qualifies, and a task waits rather than run against a metered account with no headroom or one whose figure cannot be read (ADR-0035 point 3). A secret the task references is resolved at run from the orchestrator config that holds it, extending ADR-0015 point 4's split (the credential lives in the orchestrator config, not the agent) to a task's secret, materialized as a scoped temporary environment for that run alone and removed when the run ends including on a crash or a kill; the task file holds only the reference, never the secret's value, and the value never crosses a channel. **New: the harness's record of that run goes with it.** The backend keeps its own record of a session, its transcript, and that record holds what the run's tools returned, the secret's value among them once the agent has read it. When the one-off that ran a task with a secret is killed, pao removes that agent's transcript after the harvest, which copies the working directory and not the transcript, and confirms it is gone. A removal that fails, or a transcript pao cannot find where its backend says it is, is reported to the operator, never passed over in silence. What the agent itself copies elsewhere, into its output or its working directory, stays deferred, as ADR-0036's deferred list leaves it.

## Consequences

- At the end of a run, a task's secret leaves both places the test run found it on the node user's disk: pao's run-scoped file, and now the harness's transcript of the run.
- The transcript of a run with a secret is not kept for later reading. What the run produced that matters is in the task's log and the harvest of its working directory.
- A transcript that cannot be removed, or found, is a visible failure to the operator, not a value left behind in silence. If the backend changes where it keeps transcripts, that shows at the first run with a secret.
- ADR-0036 is superseded in full. Its points 1, 2 and 4 to 6, its deferred list and its consequences carry over unchanged. Only point 3 is replaced, with the same text and the new sentences about the transcript. What the agent copies on its own, into its output or working directory, is still the deferred case.
