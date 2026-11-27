# Running pao as a service

pao runs each agent in a session of its own tmux server. When pao starts, it takes back every configured agent whose session is still running, so an agent keeps its conversation across a pao restart ([ADR-0051](../adr/0051-agents-outlive-a-pao-restart.md)). That only works if stopping pao doesn't stop the tmux server with it.

pao starts the tmux server itself the first time an agent comes up, so the server is pao's child. A service manager that stops a service's whole process group takes the server down too, and every agent with it.

## systemd

With systemd, set `KillMode=process` in pao's unit. Stopping or restarting the service then ends only pao itself, and the tmux server keeps running:

```ini
[Service]
ExecStart=/path/to/pao run /path/to/pao.yaml
Restart=on-failure
KillMode=process
```

systemd logs that it found a left-over process when the service starts again. That process is the tmux server, kept on purpose.

To end every agent as well, stop the agents first (`pao agent stop <name>`, or `pao kill` for a temporary agent, which harvests), or kill the tmux server yourself: `tmux -L <tmux_socket> kill-server`.

## What a restart keeps and what it doesn't

- **Configured agents** are taken back as they are. Their token for calling pao is kept in the state directory, readable by the node user alone.
- What an agent writes while pao is down is in its own context, but is not shown to anyone: pao follows its transcript from where it stands when pao starts again. A reply finished during a restart doesn't reach the chat. Ask again, or read the agent's terminal.
- **Temporary agents**, task one-offs included, keep running but pao no longer knows them. pao tells the operator which sessions it found and leaves them running. End one with `tmux -L <tmux_socket> kill-session -t pao-<name>`. Its working directory stays where it was.
- A pending approval question is gone. The agent's tool call was refused when pao stopped, and the agent may ask again.
- A restart of the tmux server, or of the host, still ends every agent.

## Turning an agent's own tools off

A backend ships tools of its own. Some don't fit an agent that people talk to through a chat. Claude Code's multiple-choice question (`AskUserQuestion`) and plan mode (`EnterPlanMode`, `ExitPlanMode`) open a dialog in the agent's terminal and wait there for an answer. A chat can't give that answer, so the agent waits for good.

Turn tools off for every agent in `pao.yaml`:

```yaml
profile_defaults:
  tools:
    disable: [AskUserQuestion, EnterPlanMode, ExitPlanMode]
```

This is the recommended default for any node whose agents are reached through a chat. pao turns nothing off on its own: without this block every agent has all its backend's tools.

A profile, or a recipe's profile, can give its own list. That list replaces the default; it isn't added to it:

```yaml
# profiles/notes.yaml
tools:
  disable: [AskUserQuestion, EnterPlanMode, ExitPlanMode, Bash]
```

An empty list (`disable: []`) gives that agent every tool back. Tools are named as the backend names them. With Claude Code, a turned-off tool is removed from the agent's tool list, and nothing the agent's own settings say can give it back.

A backend that can't turn its tools off refuses a profile that asks it to, whether the list comes from the profile or from `profile_defaults`. pao says so when it loads the configuration.
