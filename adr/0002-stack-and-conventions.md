# ADR 0002: Stack and conventions

**Status:** Proposed (2026-11-12)

## Context

pao is a new Go service. Before detailing components, we fix the stack and the engineering rules so later ADRs build on a known base.

## Decision

The design names only **one external dependency, the coding-agent harness** (Claude Code first, Codex later), which pao drives as its agents (ADR-0001). Beyond that it names **tmux** (the terminal driver, unavoidable to drive a terminal agent). **A mail gateway is an OPTIONAL fit, not a dependency** — a standard SMTP/POP3 gateway slots in naturally if pao does email, but pao does not need one and it can be ignored. Everything else is described by its **role**, not a product; the concrete tool is an implementation/operator choice.

### Stack
- **Language: Go**, a single binary per node.
- **The agent = a coding-agent harness** (the one named external dependency), driven terminal-in / transcript-out (ADR-0001).
- **Agent control: tmux** — the terminal driver for paste-in and lifecycle; output read from the **JSONL transcript**, never scraped.
- **Inter-node transport: pao's own.** pao provides the unified comms/transport; it is new, not a reuse. (Transport specifics: later ADR.)
- **Management UI binds to localhost by default** (first iteration). Exposing it beyond localhost is the **operator's own choice**, not prescribed by pao.
- **State/persistence:** files where files suffice, a small embedded store for structured/queryable state (audit log, id map) — a role, not a named product.
- ⏸️ **An external-API-call framework is left OUT for now** — pao's agents are coding-agent harnesses, not direct API callers. Such a framework re-enters only when pao needs an external API method (e.g. voice/STT).

### Conventions (the basic rules)
- **Do not reinvent the wheel; prefer open source and reuse where it fits** — but the design does not prescribe specific third-party tools. It names roles, the coding-agent harness, and tmux; a mail gateway is an optional fit (not required); concrete tool choices are left to the implementation/operator.
- **Pluggable over hardcoded** wherever a second implementation is foreseeable: backends, approval policy, recipes, audit sink.
- **Generic naming** for pao's own files (`.pao.*`), never a backend name.
- **ADRs** per ADR-0000: broad first, detail in successive ADRs; immutable once accepted.

## Consequences

pao reuses existing, proven building blocks instead of new code where it can, so the surface to build and maintain stays small. Swapping a piece (a backend, the transport, a sink) becomes a config or plugin change, not a rewrite.
