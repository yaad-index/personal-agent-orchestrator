<script lang="ts">
  import { createTask } from "../lib/api";
  import { list } from "../lib/tasks";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let title = $state("");
  let description = $state("");
  let done = $state("");
  let tags = $state("");
  let how = $state("");
  let parent = $state("");
  let tools = $state("");
  let scripts = $state("");
  let secret = $state("");
  let busy = $state(false);
  let error = $state("");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      const t = await createTask({
        title: title.trim(),
        description: description.trim(),
        done: done.trim(),
        tags: list(tags),
        how: how.trim() || undefined,
        parent: parent.trim() || undefined,
        attachments: { tools: list(tools), scripts: list(scripts), secret: secret.trim() || undefined },
      });
      location.hash = "#/tasks/" + encodeURIComponent(t.id);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not create the task.";
    } finally {
      busy = false;
    }
  }
</script>

<section>
  <p><a href="#/tasks">← Tasks</a></p>
  <h1>New task</h1>
  <p class="muted">An agent whose tags match picks it up. Nothing here widens what the agent may do: its tools and scripts are still bound by its access and the gate.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <form class="stack" onsubmit={submit}>
    <label for="t-title">Title</label>
    <input id="t-title" bind:value={title} required />
    <label for="t-desc">What to do</label>
    <textarea id="t-desc" class="code" rows="5" bind:value={description} required></textarea>
    <label for="t-done">Done when</label>
    <textarea id="t-done" class="code" rows="3" bind:value={done} required></textarea>
    <label for="t-tags">Tags of the agents that may run it (comma-separated)</label>
    <input id="t-tags" bind:value={tags} required />
    <label for="t-how">How (optional): the skill, tool or template it is done with</label>
    <input id="t-how" bind:value={how} />
    <label for="t-parent">Created from task (optional)</label>
    <input id="t-parent" bind:value={parent} />
    <label for="t-tools">Tools it uses (optional, comma-separated)</label>
    <input id="t-tools" bind:value={tools} />
    <label for="t-scripts">Scripts it runs (optional, comma-separated)</label>
    <input id="t-scripts" bind:value={scripts} />
    <label for="t-secret">Secret it needs (optional): its name, never its value</label>
    <input id="t-secret" bind:value={secret} autocomplete="off" />
    <button type="submit" disabled={busy}>{busy ? "Creating…" : "Create task"}</button>
  </form>
</section>
