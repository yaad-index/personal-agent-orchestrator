<script lang="ts">
  import { onMount } from "svelte";
  import { node, recipes, channels, agentAction, spawn, kill, type Agent, type Recipe } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let agents: Agent[] = $state([]);
  let recipeList: Recipe[] = $state([]);
  let recipe = $state("");
  // the channel a spawned agent's output is bound to; asked only when the node
  // has more than one, as pao then needs it named
  let channelNames: string[] = $state([]);
  let channel = $state("local");
  let name = $state("");
  let busy = $state("");
  let error = $state("");
  let notice = $state("");

  async function load() {
    try {
      const [info, r, cs] = await Promise.all([node(), recipes(), channels()]);
      agents = info.agents;
      recipeList = r;
      channelNames = cs.map((c) => c.name);
      if (!channelNames.includes(channel) && channelNames.length) channel = channelNames[0];
      if (!recipe && r.length) recipe = r[0].name;
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the agents.";
    }
  }

  onMount(load);

  // run does one action on an agent, shows how it went, and reloads.
  async function run(key: string, what: () => Promise<string>) {
    busy = key;
    error = "";
    notice = "";
    try {
      notice = await what();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "That did not work.";
    } finally {
      busy = "";
      await load();
    }
  }

  const act = (a: Agent, action: "start" | "stop" | "restart") =>
    run(a.name + action, async () => {
      await agentAction(a.name, action);
      return `${a.name}: ${action === "stop" ? "stopped" : action === "start" ? "started" : "restarted"}.`;
    });

  function remove(a: Agent) {
    if (!confirm(`Kill ${a.name}? Its outputs are harvested first, then its working directory is removed.`)) return;
    run(a.name + "kill", async () => {
      const res = await kill(a.name);
      return `${a.name} killed. Harvest ${res.harvest.id}${res.harvest.complete ? "" : " (incomplete: its working directory was kept)"}.`;
    });
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    run("spawn", async () => {
      const s = await spawn(recipe, name.trim(), channelNames.length > 1 ? channel : "");
      name = "";
      return `${s.name} spawned from ${recipe}.`;
    });
  }
</script>

<section>
  <div class="heading">
    <h1>Agents</h1>
    <button class="link" onclick={load}>Refresh</button>
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="ok" role="status">{notice}</p>{/if}
  {#if agents.length === 0}
    <p class="muted">No agents.</p>
  {:else}
    <table class="agents">
      <thead><tr><th>Agent</th><th>State</th><th class="wide-only">Kind</th><th></th></tr></thead>
      <tbody>
        {#each agents as a (a.name)}
          <tr>
            <td>{a.name}{#if a.tags.length}<div class="muted small">{a.tags.join(", ")}</div>{/if}</td>
            <td><span class="state" class:running={a.state === "running"}>{a.state}</span></td>
            <td class="wide-only">{a.temporary ? "temporary" : "configured"}</td>
            <td class="row-actions">
              {#if a.temporary}
                <button class="link danger" disabled={!!busy} onclick={() => remove(a)}>Kill</button>
              {:else if a.state === "running"}
                <button class="link" disabled={!!busy} onclick={() => act(a, "restart")}>Restart</button>
                <button class="link" disabled={!!busy} onclick={() => act(a, "stop")}>Stop</button>
              {:else}
                <button class="link" disabled={!!busy} onclick={() => act(a, "start")}>Start</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  <h2>Spawn a temporary agent</h2>
  {#if recipeList.length === 0}
    <p class="muted">No recipes on this node.</p>
  {:else}
    <form class="stack" onsubmit={submit}>
      <label for="recipe">Recipe</label>
      <select id="recipe" bind:value={recipe}>
        {#each recipeList as r (r.name)}<option value={r.name}>{r.name}{r.description ? ` — ${r.description}` : ""}</option>{/each}
      </select>
      {#if channelNames.length > 1}
        <label for="spawn-channel">Its output goes to</label>
        <select id="spawn-channel" bind:value={channel}>
          {#each channelNames as c (c)}<option value={c}>{c === "local" ? "the local channel" : c}</option>{/each}
        </select>
      {/if}
      <label for="spawn-name">Name (optional)</label>
      <input id="spawn-name" bind:value={name} placeholder="made from the recipe's name" />
      <button type="submit" disabled={!!busy || !recipe}>{busy === "spawn" ? "Spawning…" : "Spawn"}</button>
    </form>
  {/if}
</section>
