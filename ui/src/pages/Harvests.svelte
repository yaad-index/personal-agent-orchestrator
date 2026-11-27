<script lang="ts">
  import { onMount } from "svelte";
  import { harvests, type Harvest } from "../lib/api";
  import { size } from "../lib/size";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let list = $state<Harvest[] | null>(null);
  let error = $state("");

  onMount(async () => {
    try {
      list = (await harvests()).sort((a, b) => b.time.localeCompare(a.time));
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the harvests.";
    }
  });
</script>

<section>
  <h1>Harvests</h1>
  <p class="muted">What a temporary agent left in its working directory when it was killed.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if list && list.length === 0}
    <p class="muted">No harvests on this node.</p>
  {:else if list}
    <table class="trim">
      <thead><tr><th>Harvest</th><th>Agent</th><th>Files</th><th>When</th><th>Asked by</th></tr></thead>
      <tbody>
        {#each list as h (h.id)}
          <tr>
            <td><a href={"#/harvests/" + encodeURIComponent(h.id)}>{h.id}</a>{#if !h.complete}<div><span class="badge warn">incomplete</span></div>{/if}</td>
            <td>{h.agent}</td>
            <td>{h.files.length} · {size(h.bytes)}</td>
            <td class="muted">{new Date(h.time).toLocaleString()}</td>
            <td class="muted">{h.requester}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>
