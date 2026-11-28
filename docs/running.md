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

## A channel is one conversation of one owner

A chat channel is one conversation and the one person it belongs to. pao acts on a message only when it comes from that conversation and from that person. Anything else, whoever sends it and wherever it is sent, reaches no agent and is answered with nothing. That covers a group the bot was added to, another topic of the same group, the owner writing somewhere else, and anyone else writing in the channel's own conversation. Each such message is kept in the record of rejected messages, with its sender and its text, for you to read (`pao inbound rejected`, or the UI's rejected page). Everything the channel shows goes to that one conversation. pao's own questions and notices are the exception: they also go to the owner's private conversation with the bot, where nobody else reads them, and a button pressed there is the owner's answer.

```yaml
providers:                # platform connections, defined here only
  - name: bot1
    kind: bot
    credential_env: PAO_BOT_TOKEN
  - name: bot2
    kind: bot
    credential_env: PAO_TEAM_BOT_TOKEN
channels:
  - id: 1
    name: chat
    provider: bot1
    owner: alice          # the subject the channel belongs to
    target: private       # alice's private conversation with the bot
  - id: 2
    name: team
    provider: bot2
    owner: alice
    target: "-1001234567890/42"   # topic 42 of the group, or just the group's id
    groups:
      - id: "-1001234567890"
        name: team
        members: operators
```

A channel whose target is a group lists that group, and only that group, under `groups`. A channel whose target is `private` lists none. Both `owner` and `target` are required: a configuration without them doesn't load.

A provider is the connection a channel is carried on: its `kind`, its credential and the adapter's `options`. It is defined in `pao.yaml` only, never from the UI or in the file pao manages. Its credential follows the same rules a channel's did: it is named, as a variable or one of the node's secrets, never written in the file, and where it is read from is that provider's alone. For now a provider carries one channel, so a second conversation needs a second provider, and a second bot.

A channel is also one agent's: its default agent. Everything the owner writes there goes to that agent, and the channel shows that agent's words and pao's own, and no other agent's. Addressing another agent from the channel, by name or by a tag, is answered `only <agent> is reached from this channel.` and goes nowhere. A channel with no default agent reaches none. An agent belongs to a user, named in its profile:

```yaml
# profiles/notes.yaml
name: notes
user: alice      # only a channel alice owns may be this agent's
```

Setting a channel's default agent is refused unless the agent's `user` is the channel's `owner`. An agent with no `user` is no channel's. An agent is the default of one channel at a time: making it another's moves it there. pao warns at start about a channel whose owner is no subject it knows, since that channel acts on nothing until the subject is added.

## The file pao manages

pao never writes `pao.yaml`. Settings the management UI sets go in a second file, which you name in `pao.yaml`:

```yaml
managed: managed.yaml   # relative to pao.yaml's directory, or an absolute path
```

Without `managed`, pao writes no configuration and the UI changes none of those settings. The file is created on pao's first write; until then it holds nothing. It takes channels, written as in `pao.yaml`, and nothing else: any other setting in it is an error.

At start, the channels of both files are joined. A channel, group or topic defined in both, by name or by number, stops pao from loading, and the error names both files; pao never picks one. pao writes the file only with content that loads and that this build's channel adapters accept, and writes it whole, so it is never half-written. Read it, back it up or delete it as you like, but don't edit it while pao runs: pao's next write replaces it. `pao config check` names it.

## The management UI

pao serves its management UI at `/ui` on the same address as its API, `listen` in `pao.yaml`, which is on localhost by default: `http://127.0.0.1:7744/ui/` unless you changed it. Sign in with the node's operator token, the one in the variable `token_env` names. The sign-in lasts 12 hours, or until you sign out or pao restarts.

The Node page's **Restart pao** stops pao gracefully and starts it again in the same process, with the environment it was started with, so a service manager sees nothing. Agents keep running and are taken back. Restarting applies configuration pao reads only at start, and picks up a new pao binary at the same path. Everyone signs in again afterwards.

The **Agents** page starts, stops and restarts configured agents, spawns a temporary agent from a recipe, and kills one, harvesting it first. Each is the same operation as `pao agent`, `pao spawn` and `pao kill`, under the same checks: a spawn the operator holds no grant for (`pao access`) is refused here as it is at the command line. The **Approvals** page lists the questions the gate is waiting on, oldest first, and answers them: allow, always, or deny with a reason. A question answered in chat leaves the list too.

The **Tasks** page lists the tasks still open (and the ended ones when asked), shows a task with its log, creates one, and cancels one with a reason. These are `pao tasks list`, `show`, `create` and `cancel`, under the same checks: a task is created only for tags the operator holds a grant on, and a secret it names must be configured.

The **Channels** page lists every channel, from `pao.yaml` and from the file pao manages, and every provider with whether its credential is there to read. It adds, changes and removes the channels in the file pao manages, each on a provider that carries no other; those in `pao.yaml` are changed there, and without `managed` nothing is changed here. Providers are changed in `pao.yaml` only. A provider names its credential, as one of the node's secrets or a variable in pao's environment, and you put the value there yourself: the page never takes, shows or stores one. A variable counts only if it was in the environment pao was started with, because a restart from the UI starts with that one again; one set later needs the service itself started again. pao gives a new channel an id it has never used, or the id a channel of that name once had. Changes apply when pao next starts. **Restart pao** first checks that pao would start: the configuration loads, each provider's adapter accepts it, no channel id is reused, and the credential of every provider that carries a channel is there to read. If not, it says why and pao keeps running.

The **Bindings** page shows where each agent's output goes, binds an agent's replies or errors to the local channel, a group or topic, or a private conversation, and removes a binding set while pao runs; one set in the configuration file is shown and changed there. It also sets and clears the default agent of each chat channel and group. These are the same operations as `pao bindings` and `pao channel default`, under the same checks.

The **Watch** page follows an agent's output as it comes, the recent past first, at a level you pick: 1 shows its messages, 2 adds tool calls, 3 tool results and reasoning, 4 everything, unless the agent's profile maps its output to levels of its own. It is the same view as `pao watch`. Watching changes nothing for the agent or anyone else, and stopping, or leaving the page, ends it. Each agent on the Agents page has a Watch link.

The **Harvests** page lists what killed temporary agents left behind, shows a harvest's files and what was not copied, downloads a file, and deletes a harvest. A harvested file is the agent's own content, so the node serves it only as a download, never as a page. The **Rejected** page is the record of inbound messages pao did not act on, most recent first. It is read-only, and a payload is a stranger's text, shown as text.

The UI acts as the operator, so keep it on localhost. To reach it from another machine, forward the port over SSH (`ssh -L 7744:127.0.0.1:7744 <host>`) rather than binding `listen` to a public address.

### Profiles

The UI's Profiles page lists every file in `profiles_dir` and what the node does with it:
- **Loaded:** the node runs the profile as written.
- **Restart to apply:** it was saved while its agent ran. The agent keeps the profile it started with until it's restarted, which the page offers.
- **Not applied:** the file was changed outside pao since the node started. Saving it from the UI applies it.
- **Invalid:** the node wouldn't load it. The page shows why.

A profile is edited as the text of its file, so comments and layout stay as you wrote them. A save is checked exactly as loading the configuration checks it, and refused if it wouldn't load. A saved profile applies without restarting pao: a new profile becomes an agent at once, a stopped agent takes its new profile at once, and a running agent takes it when it's restarted. A profile keeps its agent's name; a new name is a new profile.

"New profile" asks for a name, a working directory, a model and tags, and fills in a starting Claude Code profile for you to read and change before it's created.
