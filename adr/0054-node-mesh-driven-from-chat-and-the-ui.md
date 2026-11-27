# ADR 0054: pao is a per-machine node mesh, driven from chat and set up from its UI

**Status:** Proposed

Supersedes ADR-0001. It keeps ADR-0001's points 0 to 4 unchanged and replaces point 5. Builds on ADR-0047 (the UI as the primary setup surface), ADR-0005 (the management surface, private and on localhost by default), ADR-0046 (who may issue which command), ADR-0029 (output bindings), ADR-0006 (temporary agents) and ADR-0040 point 4 and ADR-0041 point 4 (no new spawn path).

## Context

ADR-0001 point 5 put spawning agents and routing on chat, not on a console or dashboard, and kept the management UI to configuration and administration. ADR-0047 made the UI the primary surface for the configuration setup flow: a user defines a channel, provisions a source and picks a profile there. It left the last two steps of that flow, launching an agent and binding it to a channel, to a following ADR. Moving them onto the UI changes ADR-0001 point 5, and the index makes any change to an accepted ADR a full supersession. This is that ADR.

The aim is the one ADR-0047 named: a user reaches one surface and goes from nothing to a running agent wired to a channel, without the command line and without an agent in between. Chat stays how pao is driven day to day: talking to agents, routing, reacting and answering questions.

## Decision

Points 0 to 4 are ADR-0001's, unchanged.

0. **The agents are CLI coding agents, not the LLM API.** pao drives coding-agent CLIs (Claude Code first, others later) as the agents, deliberately not the raw API, because the coding agent brings the whole harness for free: tools, hooks, context management, file access, MCP. This is why the I/O model is terminal-paste + transcript (point 3).
1. **One node daemon per machine.** Nodes form a master/secondary mesh; the user talks only to the master, the single user-facing coordinator. (Transport and failover: later ADR.)
2. **pao is an input router.** Every input, from any source, is directed to the right agent, and **everything is a routable agent** — including a judge. There is no special fixed infrastructure; agents and a judge are all addressable routing targets. (Routing algorithm: deferred.)
3. **Agent I/O is terminal-in, transcript-out, and backend-agnostic.** `channel = human ↔ pao`; `tmux paste = pao → agent`; the agent's **transcript = agent → pao**. Channels are only between the human and pao, never between pao and an agent. Any CLI coding agent that reads a terminal and writes a transcript is a pluggable backend. (Per-backend specifics: later ADR.)
4. **Two configs.** An **orchestrator config** (sources, channels, routing, credentials) and a per-agent **profile** (agent-intrinsic only). The agent is source-agnostic: it receives `[sender]: text` and never knows the channel or transport.

Point 5 is replaced:

5. **Driven from chat; set up, launched and bound from the UI.** The user drives pao day to day from chat: talking to agents, routing, commands and reactions. The management UI is the surface a user reaches first to set pao up, and the setup flow runs through to a working agent:
   - the configuration steps of ADR-0047 point 2 (a channel, a source, a profile);
   - **launching** an agent: starting, stopping and restarting a configured agent, and spawning or killing a temporary agent from a recipe (ADR-0006);
   - **binding** an agent to a channel: its output bindings (ADR-0029) and a channel's or conversation's default agent.

   Each of these is a call to the same operation the node command line and chat already run, with the same checks (ADR-0047 points 3 and 4). A launch from the UI is not a new spawn path (ADR-0040 point 4, ADR-0041 point 4); it is the existing one, reached from another front. Who may launch and bind is unchanged: starting, stopping and restarting an agent is the operator's (ADR-0046 point 3), spawning is the operator's own spawn (ADR-0040 point 4), and binding is channel-scoped steering, the channel owner's (ADR-0046 point 3). The UI's actor is the operator for now (ADR-0047 point 4), so from the UI both are the operator's. The UI binds to localhost by default and is exposed only by the operator's own choice (ADR-0005 point 4). Chat keeps every command it has (ADR-0046); the UI adds a front and no authority.

## What this ADR deliberately does NOT decide

Kept from ADR-0001: the routing algorithm; the approval gate and its policy; the access model; temporary agents; verbosity levels and retention; the control plane (agent lifecycle, context watch, compaction); the message model and reactions/buttons; node transport and failover. Each has its own ADR since, and none is changed here.

New here, left open:
- A channel owner launching or binding from the UI. That needs a UI actor other than the operator, which waits on the UI's authentication of other subjects (ADR-0047 leaves that open).
- Answering approval questions and watching an agent's output from the UI, which are view and driving concerns beyond setup.
- The UI's framework, its screens and the order they ship.

## Consequences

A user can go from nothing to a running agent wired to a channel from one surface. That was the gap that stalled the first version and that ADR-0047 began to close. Because every UI step is an existing operation under the existing checks, the UI widens reach without widening authority: the operator can do from the UI exactly what the operator could already do at the command line and, for steering, from chat.

The founding ADR is superseded only in point 5. Points 0 to 4, the node mesh, routing, terminal-in and transcript-out, and the two configs, stand as ADR-0001 wrote them, carried here word for word so the spine has one current home.
