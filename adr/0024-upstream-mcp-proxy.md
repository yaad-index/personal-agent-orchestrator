# ADR 0024: pao as a proxy for upstream MCP servers

**Status:** Accepted (2026-11-17)

Extends ADR-0004 (the approval gate), ADR-0005 (access matrix + management surface), ADR-0012 (tag-scoped access), ADR-0016 (pao as an MCP server).

## Context

ADR-0016 made pao an MCP server: it exposes its own operations as tools, so an agent drives pao through ordinary tool use. Separately, the agents pao runs often need tools that live in other, already-running MCP servers (an upstream MCP fronting some system or data source). Today each such server would have to be configured into each agent that needs it, with that agent holding the upstream endpoint and credential, and with no way for pao to decide, per agent, which of those tools an agent may even see or call.

This ADR makes pao a proxy in front of those upstream servers. pao connects to each upstream MCP once, as a client, and re-exposes their tools through its own MCP server (ADR-0016), bounded by the same access matrix (ADR-0005, ADR-0012) and approval gate (ADR-0004) as every other surface. An agent then connects to exactly one MCP, pao's, and the set of tools it sees there grows with its access.

## Decision

1. **pao is an MCP client to upstream MCP servers and re-exposes their tools through its own MCP server.** pao holds each upstream server's configuration once (its endpoint and credential), connects to it as a client, and surfaces its tools alongside pao's own operations (ADR-0016) on pao's single MCP endpoint. An upstream server joins pao's surface by being configured in pao, not in any agent.

2. **An agent connects to exactly one MCP: pao's.** An agent never configures or connects to an upstream server directly. It holds no upstream endpoint and no upstream credential; pao holds those, once, and the agent reaches an upstream tool only as a tool on pao's surface.

3. **Each upstream server, and each of its tools, is a resource in the access matrix.** A proxied tool is a tool resource in the matrix exactly as ADR-0005 and ADR-0012 already define tool resources: default-deny, grantable by tag-scope or by individual cell. An agent's reachable tools are the union of its scopes and cells over the proxied set.

4. **An agent sees only the proxied tools its access grants, and an ungranted tool is not revealed.** The tool list pao presents to an agent contains only the proxied tools that agent is granted, so "more tools" is exactly "more access." A tool the agent is not granted is absent from its list, not listed-and-refused, so the proxy never discloses the existence of tools out of reach, matching ADR-0012 point 4 (a resource out of reach is indistinguishable from one that does not exist).

5. **Every proxied call passes pao's access check and approval gate before pao forwards it upstream.** A call to a proxied tool is authorized against the caller's grants (ADR-0005, ADR-0012) and judged by the gate (ADR-0004), as the same tool would be on any other surface, and only then does pao make the upstream call. The proxy is an entry into pao's authorization pipeline (ADR-0016 point 4), never a path around it: pao mediates every proxied call rather than tunnelling it.

6. **pao calls upstream as itself, and the per-agent decision is made inside pao.** The upstream server sees pao as its single client and knows nothing of pao's subjects; the per-agent authorization of points 3 to 5 is decided within pao before the upstream call is made. So one upstream credential serves every permitted agent, and an agent's reach is shaped by the matrix, not by the upstream server.

7. **A grant is keyed to a proxied tool's pao-side identity, and the operator assigns its tags.** A proxied tool is identified in the matrix by the identity pao gives it: its upstream server together with the tool's name as pao configures it, not the upstream server's own definition. The operator assigns that tool's tags and grants through the management surface (ADR-0005), as for any resource. Because the key is this configured identity, a grant persists across an upstream server redefining a tool under the same name; pao does not by itself detect such a redefinition, a risk the Consequences note.

## What this ADR deliberately does NOT decide

The MCP client transport and protocol specifics, and how pao maintains upstream connections (their lifecycle, caching, and failure handling). How a proxied tool is named within pao's surface to avoid collisions across upstream servers. How an upstream server's configuration and credential are stored and administered (the management surface of ADR-0005 is the home; the detail settles in code). The risk-level mapping of a proxied tool for the gate (ADR-0004 policy, not fixed here). Whether proxying an upstream server is always-on or a per-server toggle. Whether a proxied tool's schema is passed through unchanged or may be narrowed by pao. How the untrusted content an upstream server supplies, a proxied tool's own description and the results it returns, both reaching an agent unchanged, is treated as an injection surface.

## Consequences

An agent reaches every tool it is permitted from one place, pao's MCP surface, without holding any upstream endpoint or credential, and the operator widens or narrows what an agent can do by changing its grants rather than by reconfiguring the agent. Because the proxy routes every call through the same matrix and gate as the other surfaces, it adds reach without adding authority, the property ADR-0016 gives pao's own operations, now extended to upstream tools. Centralizing the upstream configuration in pao means a credential is held once and never handed to an agent. That single credential is also the cost of this design: pao's own per-agent decision is all that separates one agent's reach from another's, so a flaw in that decision exposes the whole of the credential's scope, and the upstream server, which sees only pao, can neither attribute a call to an agent nor revoke one agent's access. For the same reason that a grant is keyed to a tool's configured identity, an upstream server that redefines a tool under the same name changes what a standing grant authorizes without pao detecting it.

Because an agent's visible tool set is exactly its granted set, this is the first surface whose usefulness requires the access matrix to be enforced for tool resources, not only defined (ADR-0005, ADR-0012). It therefore lands with, or after, the work that makes matrix enforcement real; until then the proxy can carry only a trivial all-or-nothing grant, which is a placeholder and not the decision recorded here.
