# ADR 0016: pao as an MCP server

**Status:** Proposed (2026-11-13)

## Context

pao can be driven two ways so far: by a human over a communication channel (ADR-0009), and through the management interface and its HTTP API (ADR-0005). Both are operated by a human. But an agent that already uses tools has no first-class way to operate pao itself, for example to run a helper agent, hand off to another, or tear a temporary one down. This ADR adds a third interface: pao exposes its own operations as an MCP server, so driving pao becomes ordinary tool use.

## Decision

1. **pao exposes its operations as an MCP server.** Its command set (ADR-0003 routing, ADR-0006 and ADR-0007 lifecycle: run an agent, talk to one, pause or resume, kill a temporary one, and so on) is offered as callable MCP tools. This is a third interface onto pao, alongside the channels (ADR-0009) and the management interface and API (ADR-0005).

2. **Its purpose is to let an agent drive pao, not only a human.** An agent can operate pao the same way it uses any other tool, which is how an agent (not just the operator) spawns, addresses, and tears down other agents.

3. **A call carries its caller's subject identity and is authorized as that subject.** The caller of an MCP tool is a subject in the access matrix (ADR-0005, ADR-0012) like any other, and the call is bounded by that subject's grants. The MCP surface grants no authority the caller does not already hold.

4. **Every MCP tool call passes the same access matrix and approval gate as any other interface.** An operation invoked over MCP is authorized by the matrix (ADR-0005, ADR-0012) and judged by the gate (ADR-0004) exactly as the same operation would be from a channel or the API. The MCP surface is an entry point into pao's existing authorization pipeline, never a path around it.

## What this ADR deliberately does NOT decide

The exact set of exposed tools and their schemas and signatures, which are implementation detail and settle in the code rather than ahead of it. The MCP transport and protocol specifics. How a connecting caller is authenticated and resolved to a matrix subject (the mechanism; point 3 fixes only that a call is authorized as its caller's subject). Whether the MCP server is always on or is itself a toggle, and how it is exposed on a node.

## Consequences

An agent gains a first-class way to operate pao, so agent-driven orchestration (an agent spawning and coordinating others) works through the same tool-use it already does, rather than needing a human at a channel. Because every call runs through the same matrix and gate as the other interfaces, the new surface adds reach for agents without adding authority: a tool call can do only what its caller's grants allow and is still gated. Because the tool set and protocol are left to the code, the MCP surface can track pao's command set as it settles instead of being fixed ahead of it.
