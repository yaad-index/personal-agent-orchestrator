# Tasks

A task is a unit of work pao dispatches to an agent (ADR-0036). This page covers what an operator sets up so tasks can run. The model itself is in the ADR.

## Creating one

```
pao tasks create --title "Weekly report" --description "Write it." --done "It is posted." --tag reports
pao tasks list
pao tasks show <id>
pao tasks cancel <id> --reason "not needed"
```

Agents create, read and report on tasks over pao's MCP tools: `create_task`, `list_tasks`, `get_task`, `report_task`, `cancel_task`. Every tag a task carries must be one its creator's own access reaches.

## Where it runs

pao gives an open task to an agent that:
- carries every one of its tags;
- the task's owner may reach;
- holds no other task;
- has budget headroom by its own profile's rule (see [budget.md](budget.md)).

With no such agent, pao starts a one-off from a recipe whose tags cover the task's and whose account has headroom. The one-off is killed when the task is over, and what it made is harvested. If neither is possible, the task waits, and its log says why.

## Secrets

A task can name a secret configured on the node:

```yaml
secrets:
  - name: report-api
    env: PAO_SECRET_REPORT        # or: file: /path/to/file
```

- **Only on a one-off.** A task with a secret runs only on a one-off started for it.
- **The file and its path.** pao puts the value in a file only the node user can read, outside the agent's working directory. The agent's brief names that file's path, which is also in `PAO_SECRET_<NAME>`. The value itself is never in the brief, the task file or any channel.
- **Wiped.** The file is removed when the agent's session ends, and any left over is removed when the node starts.
- **The recipe must grant a file-reading tool.** The agent reads the value from the file, so the one-off's recipe has to let it use one: the harness's Read tool, or a shell. A recipe that denies them all leaves the agent unable to use its secret, and it will report the task blocked.
- **No proxied tool alongside.** A task with a secret can't also attach one of pao's proxied tools, because a new agent reaches none of them. Such a task is refused when it's created.

### An injected secret

A secret can instead be put into the agent's calls, so the agent never holds its value (ADR-0049). The operator names the hosts it may go to and the variable the agent gets a placeholder in:

```yaml
secrets:
  - name: acme-token
    env: PAO_SECRET_ACME
    inject:
      hosts: [api.acme.example, acme.example]
      env: ACME_TOKEN
```

- **A placeholder, not the value.** The agent's `ACME_TOKEN` holds a placeholder. pao's proxy replaces it with the real value in a request header on its way to one of the listed hosts, and puts it back if a response echoes it. The placeholder works nowhere else. The brief names the variable and the hosts, never a file.
- **The hosts are the operator's.** A task names only the secret. Where the value may go is set here, because a task can be written by another agent.
- **Only where the node can confine.** Every one-off runs behind the egress plane when the node allows unprivileged user namespaces: its only way out is its own proxy, and pao's state is hidden from it. Where the node cannot confine, a task with an injected secret is refused, and any other one-off runs unconfined, and the operators are told so.
- **What a one-off may reach.** Behind the plane a one-off reaches HTTPS on port 443, and only to its backend's own API, the hosts its recipe's profile names, and its secret's hosts. Anything else is refused, including git over ssh. Add a host the recipe's tasks need, a package registry say, under the profile:

```yaml
profile:
  egress:
    pass: [registry.acme.example]
```
