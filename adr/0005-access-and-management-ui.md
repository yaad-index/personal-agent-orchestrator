# ADR 0005: Access model and management UI

**Status:** Proposed (2026-11-13)

## Context

ADR-0004 gates each action at the moment it runs. Separately, pao needs to say which agents are allowed to reach which resources at all, and it needs one surface for the operator to configure that and the rest of pao. This ADR fixes the access model and where it is administered.

## Decision

1. **Access is an explicit matrix, default-restricted.** Not a per-agent on/off flag but a matrix: rows are agents (temporary agents included), columns are the resources an agent may reach (other agents, tools, channels). The operator enables or disables each cell. The default is no access; an agent reaches a resource only where a cell grants it.

2. **This is a different axis from the approval gate (ADR-0004).** The matrix is static authorization, who may touch what at all; the gate is the per-action decision, whether a specific action may run now. An action must pass both.

3. **The matrix and the rest of pao's configuration are administered through pao's own management surface: a web app plus an HTTP API.** This is the configuration and control plane, not the way pao is driven day to day, which is chat (ADR-0001).

4. **The management surface is private and access-gated, and binds to localhost on the master node by default.** It is not exposed automatically. Exposing it beyond localhost is the operator's own choice and is not prescribed by pao.

## What this ADR deliberately does NOT decide

The matrix's storage and schema; how a temporary agent's rows are created and torn down; the authentication on the management surface beyond "private and gated"; and any particular way of exposing it beyond localhost. Each is deferred to a later ADR or left to the operator.

## Consequences

Authorization is one explicit, auditable matrix with a safe default, configured in one place. A new agent starts with no access and is granted exactly the cells it needs. The two safety axes stay separate and composable: the matrix decides what an agent may ever touch, the gate decides whether a given action runs.
