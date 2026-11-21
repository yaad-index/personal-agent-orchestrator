# ADR 0025: An agent's context comes from its profile

**Status:** Accepted (2026-11-18)

**Superseded-by:** ADR-0033 (a provisioned working directory's settings are not loaded, and trust is answered)

Extends ADR-0011 (pluggable backends), ADR-0015 (profile inheritance).

## Context

pao starts a backend as the node's operating-system user, in the profile's working directory. A coding-agent backend such as Claude Code does more than run there: it loads context from that user's environment. That means instruction files in the user's home and in every directory above the working directory, MCP servers configured for the user and connectors of the user's account, the user's skills, commands and plugins, the user's settings (hooks and environment among them), and memory the backend keeps on its own.

So an agent pao exposes to chat, and that other subjects may address, can carry the instructions, tool endpoints and connected accounts of whoever runs the node. The approval gate (ADR-0004) stops an inherited tool from being called without approval. It does not stop the agent seeing the inherited context, quoting it or describing it. A profile can shut this out by hand today, with the right backend options and a settings file in its working directory, but nothing makes it so, and a profile that misses one of them leaks quietly.

## Decision

1. **By default an agent's context comes only from its profile and from pao.** A backend is started so that it loads its profile's own working directory (its instructions and settings there), the MCP configuration pao gives it, and what pao installs itself (the gate's hook). It loads nothing from the node user's own environment: no user-level or ancestor instruction files, no user-scope MCP servers or account connectors, no user skills, commands or plugins, no user settings, and no memory the backend keeps across sessions.

2. **Isolation belongs to the backend adapter, not to each profile.** The adapter for a backend (ADR-0011) owns the launch options and settings that give point 1, and adds them to every start. A profile names the backend program and its own options. A profile that sets one of the options the adapter owns is refused when the configuration is checked, never merged or silently overridden, so what an agent loads is decided in one place.

3. **A profile opts into the user's context by name, one category at a time.** The categories are `instructions` (user-level and ancestor instruction files), `mcp` (user-scope MCP servers and account connectors), `skills` (skills and commands), `settings` (the user's settings files, with the hooks and plugins they enable) and `memory` (the backend's own memory). A profile lists what it inherits. Each name opens that category and nothing else. With no list, it inherits nothing. A child profile (ADR-0015) inherits its parent's list as it inherits other fields.

4. **A backend whose adapter cannot isolate a category says so.** An adapter declares which categories it can shut out. pao refuses to start a profile on a backend that cannot shut out a category the profile does not inherit, and the configuration check names the category. A guarantee pao cannot keep is never reported as kept.

5. **The backend contract test covers isolation.** The contract test of ADR-0011 gains a case: an agent started under a user environment that holds something in every category reaches none of it, and one that inherits a category reaches exactly that one.

## What this ADR deliberately does NOT decide

The options and settings each adapter uses. Isolation from the operating system, that is, other users' files and other processes' environments: that is the same-user limit `SECURITY.md` states, and it stays there. The backend's first-run setup, such as accepting a new working directory as trusted, which is a separate change to state the backend keeps for the user.

## Consequences

What an agent knows and can reach is set by its profile and its grants, and stays so when the node's user changes their own environment. Every existing profile that adds the isolation options or the hook settings itself is refused until those are removed, a one-time change on upgrade. Where a backend cannot tell the user's context from the profile's own within one category, isolating it shuts out both. For Claude Code that is skills and commands: with `skills` not inherited, the profile's own skills are off too. A test that brings real context into a real backend needs that backend's credentials, so the contract test's isolation case runs where those exist, not in every build.
