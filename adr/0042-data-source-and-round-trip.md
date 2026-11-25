# ADR 0042: The data source a task runs on, and its versioned, non-destructive round-trip

**Status:** Proposed (2026-11-25)

## Context

ADR-0039 named the data source as driver plane 4: the working material a task runs on, provisioned by the orchestrator. ADR-0006 point 5's recipe already names a workdir source for a temporary agent, and ADR-0006 point 4 harvests a killed agent's outputs to a shared store. What is not yet fixed is where the working material comes from and in what forms, who fetches it, and what becomes of the agent's changes to it when the task ends.

This ADR draws plane 4 and fixes the write-back. It is one of the three driver planes ADR-0039 left to later ADRs, and it follows that ADR's plane discipline: a narrow interface, a simple first implementation, and a contract test.

## Decision

1. **A task's working material is a data source, provisioned by the orchestrator through a driver.** The data-source plane (ADR-0039 plane 4) is reached through a narrow driver interface, never a concrete store. The orchestrator, not the agent, fetches and prepares the source and runs the agent on top of it, extending ADR-0006 point 5's workdir source and ADR-0040's rule that the orchestrator provisions. A task-agent is not vault-backed (ADR-0040); this provisioned source is its working material.

2. **The first sources are a git repository, a folder within one, or an archive.** A driver provisions one of: a whole git repository; a folder inside a repository, so one repository can hold many folders worked independently; or an archive fetched and extracted from an object store or a downloadable file. The **simple first implementation** (ADR-0039) is the local git case, which pao can run with no external dependency; an object store is a later driver behind the same interface. Each driver passes the plane's **contract test**.

3. **The round-trip is versioned and non-destructive.** When the agent finishes, the orchestrator compares the working material against what it provisioned, per file, and where it changed it writes the change back to the source's origin without destroying the prior state: a git source is committed back, so the repository grows; an archive source is written as a **new version beside the old**, never overwriting it. Nothing the source held before is deleted.

4. **The round-trip is a per-task toggle, default on.** Write-back defaults on. It is turned off for a task whose agent owns its own outbound workflow, for example one that opens its own pull request against the source: there the agent, not the orchestrator, returns the work, and a second write-back would duplicate it.

5. **The round-trip is distinct from the harvest.** ADR-0006 point 4's harvest copies a killed agent's outputs to a shared store for other agents to read; the round-trip returns the changed working material to the source's own origin. The two have different destinations, and both may apply to the same run.

The internals left open: the exact change detection (per-file hashing in the style of ADR-0039, or the source's own diff); the archive versioning scheme and where new versions are kept; how a folder-in-a-repository source is isolated to its folder; and how a provisioning or a write-back failure is surfaced, to which ADR-0039's declare-so stance applies. The memory and connectivity planes are drawn in their own ADRs.

## Consequences

A task's working material becomes first-class and swappable: the same task shape runs on a repository, a folder, or an archive by configuration, and the orchestrator owns both the fetch and the write-back, so the agent neither reaches the store directly nor decides how its work is returned. Non-destructive versioning means a source accumulates rather than being overwritten, so a bad run costs a new version, not lost history. The default-on toggle keeps the common case automatic while letting an agent that runs its own git workflow opt out. This **builds on ADR-0039** (plane 4 and the driver discipline), **ADR-0040** (the orchestrator provisions, and a task-agent is not vault-backed), **ADR-0006** (the recipe's workdir source and the harvest), and **ADR-0037** (the task); it supersedes none of them.

The cost is an interface wide enough for a repository, a folder, and an archive yet narrow enough to stay swappable, plus change detection that is correct for each source kind. The mitigation is the simple-first discipline of ADR-0039: prove the git case with no external dependency before an object-store driver is drawn.
