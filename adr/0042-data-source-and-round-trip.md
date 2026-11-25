# ADR 0042: The data source a task runs on, and its versioned, non-destructive round-trip

**Status:** Proposed (2026-11-25)

## Context

ADR-0039 named the data source as driver plane 4, and listed the data round-trip among the things pao's own core owns. ADR-0006 point 2 already provisions a temporary agent's workdir from a source, a git clone or pushed files, and ADR-0006 point 4 harvests a killed agent's outputs to a shared store. What is not yet fixed is the forms a source may take, and what becomes of the agent's changes to it when the task ends.

This ADR draws plane 4 and fixes the write-back, following ADR-0039's plane discipline: a narrow interface, a simple first implementation, and a contract test.

## Decision

1. **pao provisions a task's working material as a data source, through a driver.** The data-source plane (ADR-0039 plane 4) is reached through a narrow driver interface, never a concrete store. **pao**, whose core owns the data round-trip (ADR-0039), not the agent, fetches and prepares the source and runs the agent on top of it, extending ADR-0006 point 2's workdir provisioning. A task-agent is not vault-backed (ADR-0040); this provisioned source is its working material.

2. **The sources are a git repository, a folder within one, pushed files, or an archive.** A driver provisions one of: a whole git repository; a folder inside a repository, so one repository can hold many folders worked independently; the pushed files ADR-0006 point 2 already allows; or an archive fetched and extracted from an object store or a downloadable file. The **simple first implementation** (ADR-0039) is the local git case, which pao can run with no external dependency; an object store is a later driver behind the same interface. Each driver passes the plane's **contract test**, and declares whether its source has a writable origin (point 3).

3. **The round-trip is versioned, non-destructive, and bounded by a write grant.** When the agent finishes, pao compares the working material against what it provisioned, per file, and writes the changes back to the source's origin, under the following bounds:
   - **Authority.** The write-back rides on a **write grant on that origin** under the access matrix (ADR-0005) and passes the gate (ADR-0004), bounded by the **running agent's** grant, since ADR-0037 point 2 keeps a task bound by the running agent's access matrix. A run whose agent has no write grant on that origin gets no write-back, exactly as ADR-0037 point 2 and ADR-0016 point 4 hold that a task hands an agent no capability its own grants do not already allow.
   - **Non-destructive.** A git origin is committed back, so the repository grows; a file the agent removed is removed at the tip, and git history retains it. An archive origin is written as a **new version beside the old**, never overwriting it. Nothing the source held before is lost.
   - **No writable origin.** A source that has none, a downloadable archive or pushed files, is not written back; its result is kept by the harvest (ADR-0006 point 4) alone, and its driver declares this at provisioning (ADR-0039's declare-so stance).
   - **Trigger.** The write-back fires on the agent's **done report** (ADR-0037: the agent reports and pao does not judge done), not on an arbitrary kill. A run that ends before it reports done, killed or flagged stalled (ADR-0007 point 5), is harvested (ADR-0006 point 4) but not written back, so a partial or abandoned run does not reach the origin.

4. **The round-trip is a per-task toggle, default on.** Write-back defaults on where the origin is writable. It is turned off for a task whose agent owns its own outbound workflow, for example one that opens its own pull request against the source: there the agent returns the work, and a second write-back would duplicate it.

5. **A run that resolved a secret does not bypass ADR-0037 point 3's open concern.** A default-on write-back carries the working directory to the origin, which may be shared more widely than the harvest store, so it reaches the same path ADR-0037 point 3 leaves open, what the agent copies into its working directory. This ADR does not widen that path: **write-back is off for a run that resolved a secret** (ADR-0037), so its working directory does not reach the origin. ADR-0037 point 3's deferred item may later refine this to a gated write-back; until it settles, off is the rule.

6. **The round-trip is distinct from the harvest.** ADR-0006 point 4's harvest copies a killed agent's outputs to a shared store for other agents to read; the round-trip returns the changed working material to the source's own origin. The two have different destinations, and both may apply to the same run.

The internals left open: the exact per-file change detection; the archive versioning scheme and where new versions are kept; how a folder-in-a-repository source is isolated to its folder; how a write-back whose origin has **moved since provisioning** is handled, which a non-destructive round-trip cannot force and so is surfaced as a declared failure (ADR-0039); and the secret-run treatment of point 5, settled with ADR-0037 point 3. pao keeps local-path provisioning off by default, so a pulled recipe cannot copy a repository off the machine; the simple-first local git case sits behind that guard as configuration, not a loosening of it. The memory and connectivity planes are drawn in their own ADRs.

## Consequences

A task's working material becomes first-class and swappable: the same task shape runs on a repository, a folder, pushed files, or an archive by configuration, and pao owns both the fetch and the write-back. When write-back is on, the agent neither reaches the store directly nor decides how its work is returned; an agent that runs its own workflow opts out and returns the work itself. Non-destructive versioning means a writable source accumulates rather than being overwritten, so a bad run costs a new version, not lost history, and bounding the write-back by a grant keeps it from reaching an origin the task could not already write. This **builds on ADR-0039** (plane 4, the driver discipline, and pao's core ownership of the data round-trip), **ADR-0040** (a task-agent is not vault-backed), **ADR-0006** (the workdir source and the harvest), **ADR-0037** (the task, its owner and its grants), and **ADR-0005 and ADR-0016** (the write authority); it supersedes none of them.

The cost is an interface wide enough for a repository, a folder, pushed files, and an archive yet narrow enough to stay swappable; change detection correct for each source kind; and a write-back that must check a grant and a moved origin before it writes. The mitigation is ADR-0039's simple-first discipline: prove the local git case, grant and all, before an object-store driver is drawn.
