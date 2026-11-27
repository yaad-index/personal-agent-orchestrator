# ADR 0051: Agents outlive a pao restart

**Status:** Proposed

Extends ADR-0007 (the control plane). Builds on ADR-0002 (the terminal driver), ADR-0006 (temporary agents and their explicit kill), ADR-0016 (pao's MCP surface, which an agent calls as itself), ADR-0037 (tasks and their one-off agents) and ADR-0049 (the egress plane).

## Context

An agent's context is the conversation it holds. A coding-agent harness keeps that only while its process runs, so an agent that is started again begins empty.

pao runs every agent in a terminal multiplexer session (ADR-0002), and ADR-0007 point 1 gives the control plane its lifecycle. Starting an agent already reuses a session that exists. In practice every agent is started again whenever pao itself restarts: to deploy a new build, to load a changed profile or after a crash. The multiplexer's server is a child of pao, so whatever stops pao stops it too, and every agent with it. An operator who upgrades pao loses every conversation in flight.

Keeping the session alive is not enough on its own. An agent calls pao's MCP surface as itself (ADR-0016) with a token pao mints when the session starts and hands it in the session's environment. A pao that starts again mints new tokens, so an agent whose session survived would be refused by the pao it reached. Some of an agent's surroundings live only in the pao process, too: a pending gate question, the origin a turn's reply goes back to, and a task one-off's egress proxy with its injected credential and per-run authority (ADR-0049). Those cannot survive the process, and this ADR says what happens to them.

## Decision

1. **An agent's session outlives pao.** The multiplexer server that holds the agents' sessions is not stopped when pao stops. Stopping pao, restarting it or replacing its binary leaves every agent running. Only an explicit stop, restart or kill of an agent ends its session (ADR-0007 point 5, ADR-0006 point 3).

2. **On start, pao adopts the agents it has a lasting record of.** Those are a configured agent, whose profile is in the node's configuration, and a task one-off, whose run is in the task store (ADR-0037). For each such agent with a live session, pao takes the agent back as it is: it registers it, follows its transcript again from where it stands and delivers to it, and does not start it again. A temporary agent spawned from a recipe (ADR-0006) is known only to the pao process that spawned it, so after a restart its live session is treated as unknown. Any session pao has no record for is reported to the operator and left running, never killed.

3. **An agent keeps its identity across a restart.** The token an agent calls pao with lasts for its session, not for one run of pao. pao keeps it in its state, readable by the node user alone, and an adopted agent's calls are accepted as before. A token is handed to a session without appearing in any process's command line.

4. **What lived only in the old process is lost, and the loss is stated.**
   - A gate question that was pending is gone. The agent's tool call was refused when pao went away, as the gate fails closed (ADR-0004), and the agent may ask again.
   - A reply whose origin was lost goes where an agent's output goes when it has no origin (ADR-0029).
   - A task one-off behind the egress plane gets its proxy opened again with the same policy, so its pass hosts work. An injected credential cannot come back: its value and the per-run authority lived only in the old process (ADR-0049). Such a one-off is flagged to the operator as running without its credential and left running for an explicit kill, which harvests first (ADR-0006 points 3 and 4). It is never reaped on the restart.

## What this ADR deliberately does NOT decide

- How the multiplexer server is kept apart from pao on a given host (a service manager setting, a separate unit, a launcher). That is deployment, and the docs say how.
- Keeping a lasting record of a running temporary agent so that it, too, can be adopted. Until that is decided, a temporary agent outlives a restart only as a reported session the operator can inspect and kill.
- Adoption across nodes. Each node adopts its own agents (ADR-0013 point 4).
- Whether a one-off's injected credential may be resolved again after a restart. ADR-0049 keeps the value in one process's memory, and this ADR does not reopen that.
- A restart of the multiplexer server itself, or of the host, which still ends every session. Resuming a harness's own saved conversation in a new session is a backend matter, not this ADR's.

## Consequences

Deploying pao, or changing a profile that needs a restart, no longer costs a configured agent or a task one-off its conversation. A temporary agent from a recipe still loses its place in pao, though its session is kept for the operator to look at. A profile change that alters an agent's own start command still takes effect only when that agent is restarted, now an explicit act, so the operator chooses when an agent gives up its context.

pao's state now holds each running agent's token, readable by the node user alone. A token handed through a session's environment no longer passes through a command line, where any local user could read it.

A one-off that ran with an injected credential and lived through a restart stays visible as degraded until someone kills it. That trades a silent loss for an open item, as ADR-0025 point 4 asks: a guarantee pao can no longer keep is not reported as kept.
