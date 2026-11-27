<script lang="ts">
  import { onMount } from "svelte";
  import { profile, saveProfile, createProfile, restartAgent, type Profile } from "../lib/api";
  import { statusLabel, statusHelp } from "../lib/status";

  // file is the profile to edit; empty to create one from draft.
  let { file, draft = "", onLost }: { file: string; draft?: string; onLost: (err: unknown) => void } = $props();
  let text = $state("");
  let saved: Profile | null = $state(null);
  let error = $state("");
  let notice = $state("");
  let busy = $state(false);

  onMount(async () => {
    if (!file) {
      text = draft;
      return;
    }
    try {
      const p = await profile(file);
      text = p.text;
      saved = p;
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the profile.";
    }
  });

  async function save() {
    busy = true;
    error = "";
    notice = "";
    try {
      if (file) {
        saved = await saveProfile(file, text);
      } else {
        saved = await createProfile(text);
        location.hash = "#/profiles/" + encodeURIComponent(saved.file);
        return;
      }
      notice = saved.status === "loaded" ? "Saved and applied." : "Saved.";
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not save.";
    } finally {
      busy = false;
    }
  }

  async function restart() {
    if (!saved) return;
    busy = true;
    error = "";
    notice = "";
    try {
      await restartAgent(saved.name);
      saved = await profile(saved.file);
      notice = "Restarted with the saved profile.";
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not restart the agent.";
    } finally {
      busy = false;
    }
  }
</script>

<section>
  <p><a href="#/profiles">← Profiles</a></p>
  <div class="heading">
    <h1>{file ? saved?.name || file : "New profile"}</h1>
    {#if saved}<span class="badge" class:warn={saved.status !== "loaded"}>{statusLabel(saved.status)}</span>{/if}
  </div>
  {#if saved && statusHelp(saved.status)}<p class="muted">{statusHelp(saved.status)}</p>{/if}
  {#if saved?.error}<p class="error">{saved.error}</p>{/if}
  <label for="profile-text">{file ? file : "Profile"}</label>
  <textarea id="profile-text" class="code" spellcheck="false" rows="22" bind:value={text}></textarea>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="ok" role="status">{notice}</p>{/if}
  <div class="actions">
    <button onclick={save} disabled={busy || !text.trim()}>{busy ? "Working…" : file ? "Save" : "Create"}</button>
    {#if saved && saved.status === "restart_to_apply" && saved.running}
      <button class="secondary" onclick={restart} disabled={busy}>Restart {saved.name}</button>
    {/if}
  </div>
</section>
