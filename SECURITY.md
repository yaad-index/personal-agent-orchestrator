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
- **Upstream server credentials.** pao reads the credential of each upstream
  MCP server it proxies once at start, unsets its variable the same way, and
  presents it itself: an agent reaches an upstream tool only through pao and
  never holds the credential. The same limit applies: it keeps the credential
  out of what agents inherit, not out of reach of another process of the user.
- **Agent tokens.** Each agent session gets a token it uses to call pao as
  itself (the MCP surface). It ties the identity to a live session and ends
  with it, so a token from a finished session is useless and a name alone is
  never taken as identity. A process of the same user that can read an agent's
  environment can use that agent's token while the session lasts.
- **Agent isolation is cooperative.** Nothing in pao stops one agent reading
  another's directory or environment if the operating system lets it.

## What an agent loads

A coding-agent backend loads context from the environment of the user it runs
as: instruction files in the home directory and above its working directory,
the user's MCP servers and account connectors, skills, settings and the
backend's own memory. That can carry the instructions, tool endpoints and
connected accounts of whoever runs the node into an agent that others talk to.
So pao starts every agent without them (ADR-0025): an agent loads its profile's
working directory, the MCP configuration pao gives it and pao's gate hook. A
profile opens a part of the user's environment by naming it under `inherit`
(`instructions`, `mcp`, `skills`, `settings`, `memory`). The backend options
that do this belong to pao: a profile that sets them itself is refused, and so
is a profile on a backend pao cannot isolate in a way the profile needs.

A backend may keep two categories in one place, so that opening one needs the
other. Claude Code reads the user's own MCP servers and skills along with the
user's settings: with `mcp` or `skills` inherited and `settings` not, the
account's connectors (or synced skills) open, and the user's own servers and
skills stay shut out. Such a coupling shuts out more than a profile asked,
never less.

This is about what the backend is told to load. It does not stop an agent that
may read files from reading the same files on purpose; that is the limit above.

## What is the boundary

The approval gate is the control on what an agent may do. A tool call the
backend's hook covers is checked against the policy: what the policy allows
runs, anything else is asked of the operator, and an unanswered or failed
question is a denial. The access matrix sits behind the operations pao exposes to
agents: an agent reaches another agent only through a grant the operator made
(`pao access grant`), and with no grants it reaches none. The tools pao offers
from upstream MCP servers are resources of the same matrix: a subject is offered,
and can call, only the ones a grant reaches, one out of reach is answered as one
that does not exist, and an agent's call is judged by the gate before pao makes
it. A tag widens who reaches an agent, so a temporary agent can be given tags
besides its recipe's at spawn only when the spawner's own tag scopes hold them.
The operator is behind
grants too: a new node's operator reaches no agent until it grants itself in
(`pao access grant operator --tag <tag>`), though administering the grants, the
subjects and the channels never needs a grant. A node that ran before the access
matrix is given, once, explicit operator grants for the tags and agents in its
configuration, listed by `pao access grants`. The grants are a file in the state
directory, read at start, so an agent the gate lets write files as the node's user
can change them for the next start, as it could the gate's policy.

**By default the gate does not guard reads.** Tools that only read (reading,
listing and searching files, and the backend loading the definitions of tools
it was given) are classified as read-only, and read-only actions
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
