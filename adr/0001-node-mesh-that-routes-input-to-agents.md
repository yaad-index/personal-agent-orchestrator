# ADR 0001: pao is a per-machine node mesh that routes input to agents

**Status:** Proposed (2026-11-12)

## Context

pao makes several independently running coding agents, across machines, into one coordinated system driven from a single front.

## Decision

The spine, and only the spine. Mechanism details are deferred to later ADRs (see below).

0. **The agents are CLI coding agents, not the LLM API.** pao drives coding-agent CLIs (Claude Code first, others such as Codex later) as the agents, deliberately not the raw API, because the coding agent brings the whole harness for free: tools, hooks, context management, file access, MCP. This is why the I/O model is terminal-paste + transcript (point 3).
1. **One node daemon per machine.** Nodes form a master/secondary mesh; the user talks only to the master, the single user-facing coordinator. (Transport and failover: later ADR.)
2. **pao is an input router.** Every input, from any source, is directed to the right agent, and **everything is a routable agent** — including a judge. There is no special fixed infrastructure; agents and a judge are all addressable routing targets. (Routing algorithm: deferred.)
3. **Agent I/O is terminal-in, transcript-out, and backend-agnostic.** `channel = human ↔ pao`; `tmux paste = pao → agent`; the agent's **transcript = agent → pao**. Channels are only between the human and pao, never between pao and an agent. Any CLI coding agent that reads a terminal and writes a transcript is a pluggable backend. (Per-backend specifics: later ADR.)
4. **Two configs.** An **orchestrator config** (sources, channels, routing, credentials) and a per-agent **profile** (agent-intrinsic only). The agent is source-agnostic: it receives `[sender]: text` and never knows the channel or transport.
5. **Driven primarily from chat.** The user drives pao mostly from chat — spawning agents, routing, commands, reactions — not a console or dashboard. The management UI is for configuration/admin only and binds to localhost by default.

## What this ADR deliberately does NOT decide

Left open for following ADRs, so this one stays minimal and immutable: the routing algorithm; the approval gate and its policy; the access model; temporary agents; verbosity levels and retention; the control plane (agent lifecycle, context watch, compaction); the message model and reactions/buttons; node transport and failover; the management UI.

## Consequences

The agents become one coordinated system behind a single chat-driven front. The spine is stable enough to build the first components against without this ADR needing edits; everything still negotiable is captured in a later, supersedable ADR.
