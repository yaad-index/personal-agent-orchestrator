<script lang="ts">
  import { onMount } from "svelte";
  import { rejected, type Rejected } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let list = $state<Rejected[] | null>(null);
  let error = $state("");
  // the most recent are shown; the rest stay in the record, a click away
  const recent = 10;
  let all = $state(false);
  const shown = $derived(list ? (all ? list : list.slice(0, recent)) : []);

  async function load() {
    try {
      list = await rejected();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the record.";
    }
  }

  onMount(load);
</script>

<section>
  <div class="heading">
    <h1>Rejected inbound</h1>
    <button class="link" onclick={load}>Refresh</button>
  </div>
  <p class="muted">Messages pao did not act on, most recent first. This is a record to read: nothing here reaches an agent, and nothing can be done to an entry.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if list && list.length === 0}
    <p class="muted">Nothing rejected.</p>
  {:else if list}
    {#each shown as r, i (i)}
      <details class="card rejected">
        <summary>
          <strong>{r.name || r.sender}</strong>{#if r.name}<span class="muted">{" "}({r.sender})</span>{/if} on {r.channel}
          <span class="muted">· {r.kind} · {r.reason}{r.count > 1 ? ` · ${r.count} times` : ""}</span>
          <div class="muted small">{new Date(r.last).toLocaleString()}{r.count > 1 ? `, first ${new Date(r.first).toLocaleString()}` : ""}</div>
        </summary>
        <pre>{r.payload}</pre>
      </details>
    {/each}
    {#if list.length > recent}
      <button class="link" onclick={() => (all = !all)}>{all ? `Show the ${recent} most recent` : `Show all ${list.length}`}</button>
    {/if}
  {/if}
</section>
