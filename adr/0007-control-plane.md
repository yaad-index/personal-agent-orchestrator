# ADR 0007: Control plane

**Status:** Proposed (2026-11-13)

## Context

pao has to run the agents it drives: bring them up, watch their health and context, and take them down. ADR-0001 established one node daemon per machine; this ADR fixes what that daemon's control plane does, covering lifecycle, context watch, and liveness. Inter-node transport and the streaming split are a separate later ADR.

## Decision

1. **The control plane owns agent lifecycle.** pao runs, kills, restarts and updates agents through the terminal driver (tmux, ADR-0002). This is the machinery every other agent action is launched through, including spawning temporary agents (ADR-0006).

2. **Startup is a per-backend procedure with a readiness timeout.** A fresh agent is brought up by its backend's startup procedure, declared in the agent profile (ADR-0001). If it has not reached readiness by a timeout, pao surfaces the raw terminal pane to the operator, shown rather than parsed or routed (no transcript exists before readiness, ADR-0002), instead of trying to enumerate every possible first-run prompt.

3. **Context watch.** pao tracks each agent's context usage and, at a configured threshold, runs a compaction flow rather than letting the agent run out of context unnoticed.

4. **Health signals are read, not self-reported.** Context usage, usage or budget, liveness and login expiry are read from the backend's own underlying source (the per-backend setting is in the profile), not from the agent reporting on itself, because a wedged agent cannot be trusted to report its own state.

5. **Liveness treats "silent" as a first-class state.** An agent that is alive but has produced no output for a while, measured from the agent's own last-output time so a clock difference between nodes does not distort it, is distinct from one that is actively working. The watchdog checks an advancing heartbeat, not whether the process is up, because a hung agent still looks up. A stalled agent of any kind is flagged, not auto-killed; removing an agent is always an explicit act, which for a temporary agent is the kill flow of ADR-0006.

6. **Periodic checks are driven by the control plane, not external schedulers.** The compaction prompts and liveness checks are the control plane's own responsibility, so they live with the thing they watch rather than in separate scheduled jobs.

## What this ADR deliberately does NOT decide

The context threshold values and the exact compaction flow; the readiness-timeout durations; the heartbeat format and the silence thresholds; how login expiry is surfaced for a manual re-login; and the inter-node streaming control/data split, which is the transport ADR's. Each is deferred.

## Consequences

pao brings agents up, watches them and takes them down through one plane, and it judges health from signals it reads itself rather than from an agent that may be wedged. "Silent" being a real state, checked by an advancing heartbeat, means a hung agent is caught instead of passing as up. Lifecycle and watch living in the control plane removes the need for separate external jobs to do them.
