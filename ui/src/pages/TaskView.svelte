<script lang="ts">
  import { onMount } from "svelte";
  import { getTask, cancelTask, type Task } from "../lib/api";
  import { terminal, warns } from "../lib/tasks";

  let { id, onLost }: { id: string; onLost: (err: unknown) => void } = $props();
  let t: Task | null = $state(null);
  let reason = $state("");
  let busy = $state(false);
  let error = $state("");

  async function load() {
    try {
      t = await getTask(id);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the task.";
    }
  }

  onMount(load);

  async function cancel() {
    if (!t || !confirm(`Cancel "${t.title}"? A one-off agent started for it is harvested and removed.`)) return;
    busy = true;
    error = "";
    try {
      t = await cancelTask(id, reason.trim());
      reason = "";
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not cancel the task.";
    } finally {
      busy = false;
    }
  }
</script>

<section>
  <p><a href="#/tasks">← Tasks</a></p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if t}
    <div class="heading">
      <h1>{t.title}</h1>
      <span class="badge" class:warn={warns(t.status)}>{t.status}</span>
    </div>
    <p class="muted">{t.id} · owner {t.owner}{t.parent ? ` · from ${t.parent}` : ""}</p>

    <dl class="facts">
      <dt>What to do</dt><dd class="pre">{t.description}</dd>
      <dt>Done when</dt><dd class="pre">{t.done}</dd>
      {#if t.how}<dt>How</dt><dd>{t.how}</dd>{/if}
      <dt>Tags</dt><dd>{t.tags.join(", ")}</dd>
      <dt>Assignee</dt>
      <dd>{t.assignee ?? "not yet assigned"}{t.one_off ? (t.removed ? " (one-off, removed)" : " (one-off)") : ""}</dd>
      {#if t.attachments?.tools?.length}<dt>Tools</dt><dd>{t.attachments.tools.join(", ")}</dd>{/if}
      {#if t.attachments?.scripts?.length}<dt>Scripts</dt><dd>{t.attachments.scripts.join(", ")}</dd>{/if}
      {#if t.attachments?.secret}<dt>Secret</dt><dd>{t.attachments.secret}</dd>{/if}
      <dt>Folder</dt><dd class="muted">{t.folder}</dd>
      <dt>Created</dt><dd>{new Date(t.created).toLocaleString()}</dd>
      <dt>Updated</dt><dd>{new Date(t.updated).toLocaleString()}</dd>
    </dl>

    <div class="heading">
      <h2>Log</h2>
      <button class="link" onclick={load}>Refresh</button>
    </div>
    {#if t.dropped}<p class="muted">{t.dropped} older entries were let go.</p>{/if}
    {#if !t.log?.length}
      <p class="muted">Nothing reported yet.</p>
    {:else}
      <ol class="log">
        {#each t.log as e, i (i)}
          <li>
            <span class="muted small">{new Date(e.at).toLocaleString()} · {e.by} · {e.kind}</span>
            {#if e.status}<span class="badge" class:warn={warns(e.status)}>{e.status}</span>{/if}
            {#if e.text}<div class="pre">{e.text}</div>{/if}
          </li>
        {/each}
      </ol>
    {/if}

    {#if !terminal(t.status)}
      <h2>Cancel</h2>
      <div class="actions">
        <input aria-label="Reason to cancel" placeholder="reason (optional)" bind:value={reason} />
        <button class="secondary danger" disabled={busy} onclick={cancel}>{busy ? "Cancelling…" : "Cancel task"}</button>
      </div>
    {/if}
  {/if}
</section>
