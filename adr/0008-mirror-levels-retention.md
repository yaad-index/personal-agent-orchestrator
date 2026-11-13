# ADR 0008: Mirror, verbosity levels, and retention

**Status:** Proposed (2026-11-13)

## Context

pao has to show what an agent is doing, reliably and at a verbosity the viewer chooses, and it has to be able to replay a past window. A mirror that depends on the agent remembering to emit, or on scraping a terminal, slips silently. This ADR fixes how pao mirrors an agent, how verbosity is controlled, and what is retained.

## Decision

1. **The mirror reads the agent's transcript, not the terminal.** pao reads the structured session transcript the backend writes (its JSONL; the path is in the agent profile, ADR-0001), never by scraping the terminal pane or trusting the agent to emit. Because the transcript captures every turn and every tool call, a turn that reaches no viewer is structurally impossible, which a scrape or an after-the-fact hook cannot guarantee.

2. **Verbosity is a per-kind level map in the agent profile.** The transcript has kinds (message, reasoning, tool call, tool result, other). The profile assigns a level to each kind, and viewing at level N shows every kind whose level is at most N. "Basic" is whatever the profile puts at the lowest level; raising the level pulls in reasoning and tool detail.

3. **A viewer can pause, resume, and backtrack with more detail.** Because the full transcript is always kept, the level controls only what is shown, live or replayed; backtracking re-renders a past window at a higher level than it was first shown.

4. **Retention is the transcript on disk.** The transcript files are themselves the full-fidelity store for replay and backtrack, so retention is largely free. The level never changes what is stored, only what is rendered.

5. **A contract test pins the transcript format.** A small test runs the real backend CLI and fails loudly if its transcript format changes, so a backend upgrade cannot silently break the mirror.

## What this ADR deliberately does NOT decide

The exact kind taxonomy beyond the five named; the level numbers and what each profile puts where; how replay is served and addressed; and the retention policy when a backend rotates or prunes its own transcript files (how pao preserves history past that). Each is deferred.

## Consequences

What an agent does is mirrored from the one source that cannot miss a turn, so a terminal-only slip is gone. Verbosity is a view, not a filter on what is captured, so a quiet stream can always be reopened in full detail. Retention costs almost nothing because the transcript is the store, and a contract test keeps a backend upgrade from breaking the mirror unnoticed.
