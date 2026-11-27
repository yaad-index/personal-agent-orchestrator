<script lang="ts">
  import { onMount } from "svelte";
  import { node, budget, type NodeInfo, type Budget } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let info: NodeInfo | null = $state(null);
  let accounts: Budget[] = $state([]);
  let error = $state("");
  let loading = $state(false);

  async function load() {
    loading = true;
    error = "";
    try {
      [info, accounts] = await Promise.all([node(), budget()]);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the node.";
    } finally {
      loading = false;
    }
  }

  onMount(load);

  function percent(w?: { used_percent: number }) {
    return w ? `${Math.round(w.used_percent)}%` : "—";
  }
</script>

<section>
  <div class="heading">
    <h1>{info ? info.node : "Node"}</h1>
    <button class="link" onclick={load} disabled={loading}>{loading ? "Refreshing…" : "Refresh"}</button>
  </div>
  {#if info}<p class="muted">Build {info.build}</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}

  <h2>Agents</h2>
  {#if info && info.agents.length === 0}
    <p class="muted">No agents are configured on this node.</p>
  {:else if info}
    <table>
      <thead>
        <tr><th>Name</th><th>State</th><th>Tags</th><th>Backend</th><th>Kind</th></tr>
      </thead>
      <tbody>
        {#each info.agents as a (a.name)}
          <tr>
            <td>{a.name}</td>
            <td><span class="state" class:running={a.state === "running"}>{a.state}</span></td>
            <td>{a.tags.join(", ") || "—"}</td>
            <td>{a.backend}</td>
            <td>{a.temporary ? "temporary" : "configured"}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  <h2>Accounts</h2>
  {#if accounts.length === 0}
    <p class="muted">No accounts.</p>
  {:else}
    <table>
      <thead>
        <tr><th>Account</th><th>State</th><th>5 hours</th><th>Week</th></tr>
      </thead>
      <tbody>
        {#each accounts as b (b.account)}
          <tr>
            <td>{b.account}</td>
            <td>{b.state}{#if b.fault}: {b.fault}{/if}</td>
            <td>{percent(b.five_hour)}</td>
            <td>{percent(b.week)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>
