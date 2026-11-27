<script lang="ts">
  import { onMount } from "svelte";
  import { harvest, deleteHarvest, harvestFileURL, type Harvest } from "../lib/api";
  import { size } from "../lib/size";

  let { id, onLost }: { id: string; onLost: (err: unknown) => void } = $props();
  let h = $state<Harvest | null>(null);
  let busy = $state(false);
  let error = $state("");

  onMount(async () => {
    try {
      h = await harvest(id);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the harvest.";
    }
  });

  async function remove() {
    if (!confirm(`Delete harvest ${id}? Its files are gone for good.`)) return;
    busy = true;
    error = "";
    try {
      await deleteHarvest(id);
      location.hash = "#/harvests";
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not delete the harvest.";
      busy = false;
    }
  }

  const name = (p: string) => p.split("/").pop() || p;
</script>

<section>
  <p><a href="#/harvests">← Harvests</a></p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if h}
    <h1>{h.id}</h1>
    <p class="muted">
      From {h.agent}{h.tags.length ? ` (${h.tags.join(", ")})` : ""} · {new Date(h.time).toLocaleString()} · asked by {h.requester}
    </p>
    {#if !h.complete}
      <p class="error">Incomplete: a limit stopped the copy, so some files were not captured. The agent's working directory was kept.</p>
    {/if}

    <h2>Files <span class="muted small">{h.files.length} · {size(h.bytes)}</span></h2>
    {#if h.files.length === 0}
      <p class="muted">No files.</p>
    {:else}
      <table class="files">
        <tbody>
          {#each h.files as f (f.path)}
            <tr>
              <td class="path"><a href={harvestFileURL(h.id, f.path)} download={name(f.path)}>{f.path}</a></td>
              <td class="muted size">{size(f.size)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="muted small">A file downloads; it is never opened here.</p>
    {/if}

    {#if h.skipped?.length}
      <h2>Not copied <span class="muted small">{h.skipped_total ?? h.skipped.length}</span></h2>
      <ul class="skipped">
        {#each h.skipped as s (s.path)}<li><code>{s.path}</code> <span class="muted">— {s.reason}</span></li>{/each}
      </ul>
      {#if (h.skipped_total ?? 0) > h.skipped.length}<p class="muted small">and {(h.skipped_total ?? 0) - h.skipped.length} more.</p>{/if}
    {/if}

    <h2>Delete</h2>
    <button class="secondary danger" disabled={busy} onclick={remove}>{busy ? "Deleting…" : "Delete harvest"}</button>
  {/if}
</section>
