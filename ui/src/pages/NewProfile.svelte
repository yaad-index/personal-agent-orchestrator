<script lang="ts">
  import { draftProfile } from "../lib/api";

  let { onDraft, onLost }: { onDraft: (text: string) => void; onLost: (err: unknown) => void } = $props();
  let name = $state("");
  let workdir = $state("");
  let model = $state("sonnet");
  let tags = $state("");
  let error = $state("");
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      const d = await draftProfile({
        name: name.trim(),
        workdir: workdir.trim(),
        model: model.trim(),
        tags: tags.split(/[,\s]+/).filter((t) => t),
      });
      onDraft(d.text);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not make the profile.";
    } finally {
      busy = false;
    }
  }
</script>

<section>
  <p><a href="#/profiles">← Profiles</a></p>
  <h1>New profile</h1>
  <p class="muted">A starting profile for a Claude Code agent. You can read and change all of it before it is created.</p>
  <form class="stack" onsubmit={submit}>
    <label for="name">Agent name</label>
    <input id="name" bind:value={name} placeholder="notes" required />
    <label for="workdir">Working directory</label>
    <input id="workdir" bind:value={workdir} placeholder="/home/you/agents/notes" required />
    <label for="model">Model</label>
    <input id="model" bind:value={model} placeholder="sonnet" />
    <label for="tags">Tags</label>
    <input id="tags" bind:value={tags} placeholder="home, writing" />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button type="submit" disabled={busy || !name.trim() || !workdir.trim()}>{busy ? "Working…" : "Continue"}</button>
  </form>
</section>
