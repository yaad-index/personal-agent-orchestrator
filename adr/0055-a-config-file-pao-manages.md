# ADR 0055: A configuration file pao writes, beside the one the operator writes

**Status:** Accepted

Extends ADR-0054 point 4 (the two configs: the orchestrator config and a per-agent profile). Builds on ADR-0047 (the UI as the primary setup surface, and its channel step), ADR-0005 (the management surface) and ADR-0046 point 5 (configuration stays off chat).

## Context

The orchestrator config is a file the operator writes by hand: `pao.yaml`, with its own comments and layout. ADR-0047 point 2 has the UI define a channel, and a channel is part of the orchestrator config. A channel defined in the UI has to outlast pao's next start, so it has to be written somewhere pao reads at start.

Writing it into `pao.yaml` would mean pao rewriting a file the operator wrote. A program that reads and writes YAML keeps the data but loses the comments and the layout, and an operator who edits the file while pao rewrites it loses one of the two edits. Profiles do not have this problem: each is its own file, and the UI writes a profile's text as given.

## Decision

1. **`pao.yaml` may name a file pao manages.** The key `managed` names it, as a path relative to `pao.yaml`'s directory or an absolute one. pao writes that file and only that file; it never writes `pao.yaml`. The operator opts in by naming the file, and can leave it out to keep every setting in their own hands.

2. **The managed file holds the orchestrator settings the UI sets, in the same form as `pao.yaml`.** Its first content is channels, written exactly as a channel is written in `pao.yaml`. Which other settings move there is decided as the UI comes to set them.

3. **At load, the two files' entries are joined, and a clash is a load error.** A channel defined in both files, by name or by number, stops pao from loading the configuration and says which, in which files. pao never picks one silently. A setting the managed file may not hold is likewise an error, not ignored.

4. **pao writes the managed file only with content that loads.** A change is checked against the joined configuration before it is written, the same way loading checks it, and the file is written whole, so it is never left half-written.

5. **Without `managed`, the UI does not change those settings.** It shows them as they are in `pao.yaml` and says how to let it manage them.

## What this ADR deliberately does NOT decide

- Where a channel's credential comes from when the UI defines one. That is a decision about secrets of its own.
- Which settings beyond channels move to the managed file, and when.
- Two UI sessions editing the managed file at once, beyond each write being whole.
- How a change to the managed file takes effect: when pao next starts, as for any configuration it reads at start.

## Consequences

The operator's own `pao.yaml` stays exactly as they wrote it, comments included, and what the UI adds lives in a file the operator can read, back up, or delete. Because a clash is an error and never a precedence, the configuration pao runs is always the one both files plainly say. The cost is a second file to know about. `pao config check` and the UI both name it, so it is not hidden.
