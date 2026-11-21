# The governor

The governor turns an operator's request, written in plain words, into exactly one pao command. pao shows the operator that command, and the operator runs it or not. The governor is a translator, not an authority: it cannot act on the node. See [ADR-0031](../adr/0031-governor-shared-on-demand.md).

```
/gpao start a helper on the acme repository
```

The governor answers where it was asked: privately, or in the group the request was sent in. The answer is either a clarifying question, or a proposal with a button: `gov proposes: /pao spawn acme-helper`. Anyone the group admits may press the button, and it runs as them.

## Setting one up

### 1. A profile

The governor is an ordinary configured agent with its own profile and working directory. Give it an inexpensive model: its job is parse, match, confirm, emit.

The start command must begin a **new session** each time, never resume an earlier one. pao starts the governor afresh whenever a request comes from a different operator than the last. A fresh context is what keeps one operator's request out of another's.

On its first start in a new working directory, Claude Code asks whether to trust the folder. No one is at the governor's terminal to answer, so it would never become ready. Accept the trust for the governor's working directory once, before its first request. One way is to start the backend in that directory yourself and confirm. Trust only that directory, not a parent of it.

### 2. Its instructions

pao writes the governor's instructions from the command table:

```
pao governor instructions /path/to/pao.yaml > /path/to/governor-workdir/CLAUDE.md
```

They list only the commands the governor may propose on this node, under the node's prefix. Run the command again after changing `governor:` in the config, or after upgrading pao.

### 3. Config

```yaml
governor:
  agent: gov                       # the profile's name
  propose: [agents, view, spawn]   # optional: a narrower set of commands
  prefix: /gpao                    # optional: the default
```

- `propose` may hold only commands a governor may propose. That excludes `help` and the gate's answers, which are the operator's alone.
- `prefix` is a `/` and one lowercase word, and never `/pao`.

### 4. Grants

```
pao access grant agent:gov --tool propose_command
pao access grant agent:gov --tag team
```

- `propose_command` is how the governor answers. Without the grant, the tool is not offered to it.
- A grant on agents lets the governor **list** them, and the recipes carrying the same tags, so it can match names. Of pao's own tools it is offered only `list_agents`, `list_recipes` and `propose_command`. pao refuses the governor every other action whatever its grants say: sending to an agent, spawning, killing, starting or stopping, watching output, reading a harvest, and calling a proxied tool.
- While it serves a request, the governor sees only what the requesting operator's own grants reach as well. With no request in hand, it sees nothing.

## How it behaves

- **Started on the first request.** The governor is not started with the node.
- **A fresh context per operator.** A request from a different operator than the last first ends the governor's session. The same operator keeps their context, so a clarifying question and its answer stay together. An answer still in flight for the previous operator is cut short.
- **It answers where it was asked.** Its replies and proposals go back to the conversation the request came from, under the same reader rule as any agent's reply. A group that may not see it is not sent it, and the reply is not sent anywhere else instead. A binding or a channel stream of the governor is refused.
- **Its notices and gate questions go to the requester alone, privately,** never to a group or to every operator. A not-ready notice carries its terminal pane.
- **Only operators** may send `/gpao`.
