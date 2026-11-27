# ADR 0053: A proxied MCP server whose credential is a secret pao holds

**Status:** Accepted

Extends ADR-0024 (pao as the MCP proxy). Builds on ADR-0037 (secrets and where they live), ADR-0049 (credential injection at the boundary, and the scrub on the way back), ADR-0044 (the tag as the grant), ADR-0012 (default-deny, and no existence leak), ADR-0004 (the gate), ADR-0042 point 5 and ADR-0043 point 4 (the rules for a run that resolved a secret), and ADR-0025 point 4 (a guarantee pao cannot keep is never reported as kept).

## Context

ADR-0024 made pao the MCP client of every upstream server. An agent reaches an upstream tool only through pao's own surface, and pao calls upstream as itself, holding the server's endpoint and credential once. It left open how that credential is stored and administered, and how what an upstream sends back is treated.

Many MCP servers worth offering need a secret: an API key or a token. If an agent connects to such a server directly, the secret has to be in the agent's configuration or environment. It is then in the agent's process, can end up in its transcript, and is readable by anything the agent runs.

ADR-0049 solved the same problem for a task's outbound HTTPS calls. The agent holds a placeholder, a proxy at the boundary fills in the real value, and an echoed value is scrubbed on the way back. An MCP server reached through pao needs no proxy in the agent's network: pao already makes the call. What it needs is for the secret to have a place in pao's configuration, to go only where it belongs, and never to come back to the agent.

The rules ADR-0037, ADR-0042 point 5 and ADR-0043 point 4 set for a run that resolved a secret were written for a task one-off that is handed the secret. A proxied server can be used by any agent granted its tools, an always-on agent included, for as long as the agent runs. Whether those rules apply has to be stated, not inherited by accident.

## Decision

1. **An upstream server names its credential as a secret, and the binding is the operator's.** In pao's configuration an upstream server names one of the node's secrets (ADR-0037) as its credential, and says how it is presented:
   - for a server reached over HTTP, in a request header of its own naming, by default `Authorization: Bearer <value>`;
   - for a local server pao starts as a process, in an environment variable of that process.

   The secret's value stays where ADR-0037 keeps a secret: in the daemon's environment or a file, never in the configuration. Only the operator binds a secret to a server. A secret bound to a server is that server's alone: no task may name it, so it is never resolved into an agent's run, and it is never presented to any other server.

2. **pao presents the secret to that server and nowhere else.** pao reads the secret when it connects to the server, and again whenever it connects anew. It keeps the value in its own memory and puts it only on its own requests to the server's configured endpoint, or in the environment of the server process it starts.
   - A redirect to any other origin is not followed with the credential.
   - The value never enters an agent's environment, configuration, MCP configuration, tool list or transcript, and pao never writes it to a log.
   - Unlike ADR-0037's per-run resolution, the secret is resolved for pao's own connection, not per run. The consumer of the credential is pao, not an agent, and a connection serves every agent granted the server's tools (ADR-0024 point 6).

3. **What comes back is scrubbed before an agent sees it.** Everything a server sends back through pao is checked for the secret's value before it is passed on, and every occurrence is replaced with a fixed marker. That covers a tool's result, an error, and a tool's description and schema. This is the scrub of ADR-0049 point 2, on pao's own client. A value the server returns transformed (encoded, split or otherwise altered) is not recognised. The Consequences say what that leaves.

4. **Granting the tools grants the tools, never the secret.** Each tool of such a server is a tool resource in the access matrix (ADR-0024 point 3), reached by the tags the operator assigns it (ADR-0024 point 7, ADR-0044). A grant to call the tool is the only access an agent gets. The secret is not a resource an agent can be granted. ADR-0044's "secret's access" is the right to resolve a task's secret, and point 1 rules that out for a secret bound to a server. Every call still passes the access check and the gate before pao forwards it (ADR-0024 point 5), and a tool out of reach is not shown (ADR-0012 point 4).

5. **Using such a tool is not a run that resolved a secret.** The rules of ADR-0037 point 3 (the transcript removed at the kill), ADR-0042 point 5 (no write-back) and ADR-0043 point 4 (no memory writes) are written for a run that resolves a secret. An agent that calls a proxied tool resolves nothing and never holds the value, so those rules do not apply, whether the agent is a one-off or always on.

   This differs from ADR-0049 point 7, which keeps the write-back and memory rules for an injected secret although that agent too holds only a placeholder. There, the task names the secret and the run exists to use it, for that run alone. Here the credential belongs to pao's standing connection to a server, not to any run. Holding every granted agent to the run rules would bar its write-back and memory for as long as it may call the server, which for an always-on agent is permanently.

   The rules apply again the moment anything gives the agent the value. A scrub that lets the value through is a defect to fix, not a mode the rules were written for.

6. **What a server sends back is the server's data, passed on as such.** A tool's result reaches the agent as that server's output, as ADR-0024 already passes it, within the limits pao sets on its size and on a description's length. pao never presents it as pao's own words and never acts on what it says. An action the agent takes because of it is an action like any other and passes the gate. The result is recorded wherever a tool's result is recorded: the agent's transcript and pao's mirror of it (ADR-0008), with the secret scrubbed. Who may call the tool, and so who may see what it returns, is the grant of point 4.

## What this ADR deliberately does NOT decide

- The configuration format beyond what point 1 names. Whether today's way of naming a credential directly by its environment variable is kept, or becomes a secret entry.
- More than one credential per server, a credential per agent or per subject, and OAuth flows. A server that needs a user's own login is not covered: point 2's one connection serves every granted agent.
- Rotation beyond reading the secret again on a new connection.
- How a local server process is confined, beyond pao starting it with the secret in its environment and outside any agent's reach.
- Recognising a transformed value in point 3's scrub.
- Whether a task one-off with a secret of its own may also be given proxied tools. The existing refusal of that pairing is unchanged here.

## Consequences

An agent can use an MCP server that needs a secret without ever holding it, and an always-on agent can do so without the restrictions written for a run that was handed a secret. The operator configures the credential once, in one place, and decides which tags reach the server's tools.

The protection rests on two things. First, pao presents the secret only to the server it is bound to. Second, the scrub catches an echoed value. A server that returns the secret transformed would get it past the scrub and into the agent's transcript. Bind a credential only to a server you trust not to do that, as with any server you give a credential to. If that happens, the run has resolved the secret after all, and point 5 says the secret-run rules then apply.

A secret bound to a server can no longer be named by a task. An operator who wants the same value in both places configures it as two secrets, which keeps each use's limits separate.
