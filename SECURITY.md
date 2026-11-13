# Security notes

pao runs coding agents that can read files and run commands, and it holds
credentials for itself. This page says what keeps an agent from acting beyond
what it was allowed, and where that stops.

## Known limitation: agents share pao's OS user

The agents pao starts run as the same operating-system user as the node. That
is a deliberate simplification, and it sets the limit of what pao can promise.

Anything the node's user can read, an agent running as that user can read if it
is allowed to run a command or open a file: the node's process environment, its
configuration, the state directory, other agents' working directories and
environments, and the local sockets. Everything below follows from that, together with the gate's default
described later: reads are not asked.

- **The operator token.** The node unsets the variable it came from before it
  starts anything, so agents do not inherit it. That keeps it out of an agent's
  environment by accident. It is not a barrier: another process of the same
  user can still read the node's own environment or any file that holds it,
  and by default an agent's read-only tools are not stopped from doing so.
- **Agent tokens.** Each agent session gets a token it uses to call pao as
  itself (the MCP surface). It ties the identity to a live session and ends
  with it, so a token from a finished session is useless and a name alone is
  never taken as identity. A process of the same user that can read an agent's
  environment can use that agent's token while the session lasts.
- **Agent isolation is cooperative.** Nothing in pao stops one agent reading
  another's directory or environment if the operating system lets it.

## What is the boundary

The approval gate is the control on what an agent may do. A tool call the
backend's hook covers is checked against the policy: what the policy allows
runs, anything else is asked of the operator, and an unanswered or failed
question is a denial. The access seam sits behind the operations pao exposes to
agents, and is deny-all for agents until the access matrix exists.

**By default the gate does not guard reads.** Tools that only read (reading,
listing and searching files) are classified as read-only, and read-only actions
are allowed without a question (`gate.auto_allow` defaults to `read`). So by
default an agent can read any file the node's user can read, including the
node's own state and, on Linux, a process's environment under `/proc`, without
the operator being asked. Commands and writes are asked. An operator who wants
reads asked too sets `gate.auto_allow` to a list that does not include `read`
(an empty list means the default), at the cost of a question for every read. Only tool calls the backend's hook covers pass the gate at all.

These are controls on what an agent is allowed to do. They are not a sandbox
around what a process that is already running can do.

### A path that stays inside pao

The gate's policy is data, so the gap above can be narrowed without leaving pao:
a policy rule can deny an agent's tool call that reads pao's configuration
directory or the running process's environment (the per-process environment
files under `/proc`), whatever the read-only default says for other paths. That
makes the gate the boundary for those reads too. It is not built yet. It would
still be a rule about the tool calls the backend's hook covers, so it narrows the
exposure and does not remove it.

## What full isolation would take

The fuller step is operating-system separation: running agents under a separate
operating-system user (or in a container or virtual machine), with the node's
files, sockets and environment unreadable to that user. pao does not do this and
does not require it; a deployment that needs it has to arrange it outside pao. Until then, treat the agents on a node as
trusted to the extent that the gate trusts them, and no further.
