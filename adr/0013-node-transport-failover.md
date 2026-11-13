# ADR 0013: Node transport and failover

**Status:** Proposed (2026-11-13)

## Context

ADR-0001 fixed a node daemon per machine in a master/secondary mesh, the human talking only to the master, and that pao provides its own inter-node transport. This ADR fixes that transport's shape and how the mesh fails over: what the transport carries, how a secondary's output reaches the human, and what happens when the master dies.

## Decision

1. **One master, the rest secondary; the human talks only to the master.** Every machine runs a node; exactly one is the master, the single user-facing coordinator and the only node bound to the human channels. The master may be a pure coordinator or also host a local agent.

2. **The transport carries coordination between nodes, not the stream.** Between nodes, pao's own transport carries only light coordination: the master directing an agent on another node (the lifecycle commands of ADR-0007, now crossing a node boundary), telling a node to stream a given agent to a given channel at a given level, and the view controls (pause, resume, backtrack). The heavy agent stream does not travel the transport. This ADR carries ADR-0007's commands across nodes; it does not redefine them.

3. **A secondary pushes an agent's stream directly to the target channel.** When the master routes an agent's output to a human channel, the secondary that runs the agent posts to that channel directly, never relaying the stream through the master or the transport. So the master decides what streams where and stays the single front, yet is not a data bottleneck.

4. **Every node carries the orchestrator config; per-agent data stays local.** Because a secondary posts directly to a human channel, it needs that channel's credentials, so every node holds the orchestrator config (sources, channels, routing, credentials). Each node's per-agent data (profiles, transcripts, message store) stays local to it.

5. **The transport is pao's own, encrypted, and the wire choice is deferred.** pao provides the inter-node transport itself rather than reusing an external bus; it is encrypted and configurable. Whether it is a broker or direct node-to-node is left open, as long as it carries the coordination of point 2.

6. **Failover is detected and announced automatically, but promotion is manual.** Secondaries detect the master's death through mesh liveness, and one announces it to the human out of band. The human then promotes a node by hand, and the promoted node takes over the human channels. Detection and the alert are automatic; promotion is deliberately a human act, so there is no automatic election to go wrong.

## What this ADR deliberately does NOT decide

The wire protocol and the transport implementation (broker versus direct, the encryption scheme). How channel credentials are distributed to the nodes and kept current. The liveness-detection and promotion mechanisms in detail, and how a demoted old master rejoins. Split-brain beyond what manual promotion avoids. How a stream is addressed and served to a channel across nodes. The single-node MVP already on main needs none of this; it arrives when the mesh is built.

## Consequences

pao scales from one machine to many without the human tracking where an agent runs: they talk to the one master, which routes, while each agent's heavy stream goes straight from its node to the channel, so the master coordinates without being a throughput bottleneck. Every node carrying the orchestrator config is the price of that direct push, and it keeps a promoted node able to take over the channels at once. Making promotion a human act trades automatic recovery for no split-brain, which suits a personal mesh where the human is reachable out of band.
