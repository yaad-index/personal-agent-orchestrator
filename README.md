# personal-agent-orchestrator (pao)

pao routes input to coding-agent harnesses across a per-machine node mesh. It is driven mostly from chat, and treats every capability — including a judge — as a routable agent.

The design is recorded as Architecture Decision Records in [`adr/`](adr/README.md): broad decisions first, detail in successive ADRs.

Status: early development. The single-node MVP is being built against the Accepted ADRs; see [AGENTS.md](AGENTS.md) for the layout and conventions.

What pao does and does not promise about keeping an agent within bounds is in [SECURITY.md](SECURITY.md).
