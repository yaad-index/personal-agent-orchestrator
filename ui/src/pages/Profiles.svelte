<script lang="ts">
  import { onMount } from "svelte";
  import { profiles, type Profile } from "../lib/api";
  import { statusLabel } from "../lib/status";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let list: Profile[] | null = $state(null);
  let error = $state("");

  onMount(async () => {
    try {
      list = await profiles();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the profiles.";
    }
  });
</script>

<section>
  <div class="heading">
    <h1>Profiles</h1>
    <a class="button" href="#/profiles/new">New profile</a>
  </div>
  <p class="muted">Each profile defines one agent: how it starts and where it works.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if list && list.length === 0}
    <p class="muted">No profiles yet.</p>
  {:else if list}
    <table>
      <thead>
        <tr><th>Agent</th><th>Status</th><th>Tags</th><th>File</th></tr>
      </thead>
      <tbody>
        {#each list as p (p.file)}
          <tr>
            <td><a href={"#/profiles/" + encodeURIComponent(p.file)}>{p.name || "—"}</a>{#if p.running}<span class="muted"> · running</span>{/if}</td>
            <td><span class="badge" class:warn={p.status !== "loaded"}>{statusLabel(p.status)}</span></td>
            <td>{p.tags.join(", ") || "—"}</td>
            <td class="muted">{p.file}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>
