# ADR 0002: Stack and conventions

**Status:** Accepted (2026-11-12)

## Context

pao is a new Go service. Before detailing components, we fix the stack and the engineering rules so later ADRs build on a known base.

## Decision

pao names just **two concrete tools, and keeps both swappable.** A **coding-agent harness** is required (it is what pao drives as its agents, ADR-0001), but which one is pluggable: any harness that meets the terminal-in / transcript-out contract qualifies, with Claude Code supported first. **tmux** is the chosen terminal driver; it is well established with no reason to switch, yet it is not special and another multiplexer could replace it. Everything else is described by its **role**, not a product, so the concrete tool stays an implementation or operator choice. Communication channels and inter-node transport each get their own later ADR.

### Stack
- **Language: Go**, a single binary per node.
- **The agent = any coding-agent harness** meeting the terminal-in / transcript-out contract (ADR-0001); the backend is pluggable, Claude Code supported first.
- **Agent control: tmux** — the chosen terminal driver for paste-in and lifecycle, swappable in principle; output read from the **JSONL transcript**, never scraped.
- **Inter-node transport: pao's own**, built new rather than reused. (Specifics: later ADR.)
- **Management UI binds to localhost by default** (first iteration). Exposing it beyond localhost is the **operator's own choice**, not prescribed by pao.
- **State/persistence:** files where files suffice, a small embedded store for structured/queryable state (audit log, id map) — a role, not a named product.
- ⏸️ **An external-API-call framework is left OUT for now** — pao's agents are coding-agent harnesses, not direct API callers. Such a framework re-enters only when pao needs an external API method (e.g. voice/STT).

### Conventions (the basic rules)
- **Do not reinvent the wheel; prefer open source and reuse where it fits** — the design does not prescribe specific third-party tools beyond the coding-agent harness and tmux, both swappable; every other concrete tool choice is left to the implementation or operator.
- **Pluggable over hardcoded** wherever a second implementation is foreseeable: backends, approval policy, recipes, audit sink.
- **Generic naming** for pao's own files (`.pao.*`), never a backend name.
- **ADRs** per ADR-0000: broad first, detail in successive ADRs; immutable once accepted.

## Consequences

pao reuses existing, proven building blocks instead of new code where it can, the inter-node transport being the deliberate exception it builds itself, so the surface to build and maintain stays small. Swapping a piece (a backend, a sink) becomes a config or plugin change, not a rewrite.
