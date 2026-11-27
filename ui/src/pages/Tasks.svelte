<script lang="ts">
  import { onMount } from "svelte";
  import { tasks, type Task } from "../lib/api";
  import { terminal, warns } from "../lib/tasks";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let all = $state<Task[] | null>(null);
  let showEnded = $state(false);
  let error = $state("");

  const shown = $derived(
    (all ?? []).filter((t) => showEnded || !terminal(t.status)).sort((a, b) => b.updated.localeCompare(a.updated)),
  );

  async function load() {
    try {
      all = await tasks();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the tasks.";
    }
  }

  onMount(load);
</script>

<section>
  <div class="heading">
    <h1>Tasks</h1>
    <a class="button" href="#/tasks/new">New task</a>
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if all}
    <label class="check"><input type="checkbox" bind:checked={showEnded} /> Show ended tasks</label>
    {#if shown.length === 0}
      <p class="muted">{all.length === 0 ? "No tasks yet." : "No open tasks."}</p>
    {:else}
      <table class="trim">
        <thead><tr><th>Task</th><th>Status</th><th>Assignee</th><th>Tags</th><th>Updated</th></tr></thead>
        <tbody>
          {#each shown as t (t.id)}
            <tr>
              <td><a href={"#/tasks/" + encodeURIComponent(t.id)}>{t.title}</a><div class="muted small">{t.id}</div></td>
              <td><span class="badge" class:warn={warns(t.status)}>{t.status}</span></td>
              <td>{t.assignee ?? "—"}</td>
              <td>{t.tags.join(", ")}</td>
              <td class="muted">{new Date(t.updated).toLocaleString()}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>
