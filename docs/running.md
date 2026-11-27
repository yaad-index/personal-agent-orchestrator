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

## The management UI

pao serves its management UI at `/ui` on the same address as its API, `listen` in `pao.yaml`, which is on localhost by default: `http://127.0.0.1:7744/ui/` unless you changed it. Sign in with the node's operator token, the one in the variable `token_env` names. The sign-in lasts 12 hours, or until you sign out or pao restarts.

The Node page's **Restart pao** stops pao gracefully and starts it again in the same process, with the environment it was started with, so a service manager sees nothing. Agents keep running and are taken back. Restarting applies configuration pao reads only at start, and picks up a new pao binary at the same path. Everyone signs in again afterwards.

The **Agents** page starts, stops and restarts configured agents, spawns a temporary agent from a recipe, and kills one, harvesting it first. Each is the same operation as `pao agent`, `pao spawn` and `pao kill`, under the same checks: a spawn the operator holds no grant for (`pao access`) is refused here as it is at the command line.

The **Tasks** page lists the tasks still open (and the ended ones when asked), shows a task with its log, creates one, and cancels one with a reason. These are `pao tasks list`, `show`, `create` and `cancel`, under the same checks: a task is created only for tags the operator holds a grant on, and a secret it names must be configured.

The **Bindings** page shows where each agent's output goes, binds an agent's replies or errors to the local channel, a group or topic, or a private conversation, and removes a binding set while pao runs; one set in the configuration file is shown and changed there. It also sets and clears the default agent of each chat channel and group. These are the same operations as `pao bindings` and `pao channel default`, under the same checks.

The UI acts as the operator, so keep it on localhost. To reach it from another machine, forward the port over SSH (`ssh -L 7744:127.0.0.1:7744 <host>`) rather than binding `listen` to a public address.

### Profiles

The UI's Profiles page lists every file in `profiles_dir` and what the node does with it:
- **Loaded:** the node runs the profile as written.
- **Restart to apply:** it was saved while its agent ran. The agent keeps the profile it started with until it's restarted, which the page offers.
- **Not applied:** the file was changed outside pao since the node started. Saving it from the UI applies it.
- **Invalid:** the node wouldn't load it. The page shows why.

A profile is edited as the text of its file, so comments and layout stay as you wrote them. A save is checked exactly as loading the configuration checks it, and refused if it wouldn't load. A saved profile applies without restarting pao: a new profile becomes an agent at once, a stopped agent takes its new profile at once, and a running agent takes it when it's restarted. A profile keeps its agent's name; a new name is a new profile.

"New profile" asks for a name, a working directory, a model and tags, and fills in a starting Claude Code profile for you to read and change before it's created.
