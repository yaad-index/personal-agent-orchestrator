<script lang="ts">
  import { onMount } from "svelte";
  import {
    channelConfig,
    putChannel,
    deleteChannel,
    restartNode,
    type ChannelConfig,
    type ChannelConfigList,
    type ChannelConfigEntry,
  } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let list = $state<ChannelConfigList | null>(null);
  let error = $state("");
  let notice = $state("");
  let busy = $state(false);
  let restarting = $state(false);

  // The form: shown to add a channel, or to change one pao manages.
  let editing = $state<"" | "new" | string>("");
  let id = $state(0);
  let name = $state("");
  let provider = $state("");
  let owner = $state("");
  // the channel's one conversation: the owner's private one, a group, or a topic of a group
  let convWay = $state<"private" | "group">("private");
  let groupId = $state("");
  let groupName = $state("");
  let topicId = $state("");

  async function load() {
    try {
      list = await channelConfig();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the channels.";
    }
  }

  onMount(load);

  function openNew() {
    if (!list) return;
    editing = "new";
    // pao gives a new channel an id never used before
    id = 0;
    name = "";
    provider = list.providers[0]?.name ?? "";
    owner = "";
    convWay = "private";
    groupId = groupName = topicId = "";
    error = "";
    notice = "";
  }

  function openEdit(c: ChannelConfigEntry) {
    editing = c.name;
    id = c.id;
    name = c.name;
    provider = c.provider;
    owner = c.owner ?? "";
    convWay = c.target && c.target !== "private" ? "group" : "private";
    const [gid, tid] = convWay === "group" ? c.target.split("/") : ["", ""];
    groupId = gid ?? "";
    topicId = tid ?? "";
    groupName = c.groups?.find((g) => g.id === groupId)?.name ?? "";
    error = "";
    notice = "";
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    const c: ChannelConfig = {
      id,
      name: name.trim(),
      provider,
      owner: owner.trim(),
      target: convWay === "private" ? "private" : groupId.trim() + (topicId.trim() ? "/" + topicId.trim() : ""),
      // a group's named topics are kept by pao; the form leaves them out
      groups: convWay === "private" ? [] : [{ id: groupId.trim(), name: groupName.trim(), members: "operators" }],
    };
    busy = true;
    error = "";
    try {
      await putChannel(c);
      notice = `${c.name} saved in ${list?.managed_file}. It applies when pao next starts.`;
      editing = "";
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not save the channel.";
    } finally {
      busy = false;
      await load();
    }
  }

  async function remove(c: ChannelConfigEntry) {
    if (!confirm(`Remove channel ${c.name}? It stops when pao next starts.`)) return;
    busy = true;
    error = "";
    try {
      await deleteChannel(c.name);
      notice = `${c.name} removed. It stops when pao next starts.`;
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not remove the channel.";
    } finally {
      busy = false;
      await load();
    }
  }

  async function restart() {
    if (!confirm("Restart pao now? Its agents keep running. You will sign in again.")) return;
    try {
      await restartNode();
      restarting = true;
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not restart pao.";
    }
  }

  function convLabel(c: ChannelConfig): string {
    if (!c.target) return "—";
    if (c.target === "private") return `${c.owner}, privately`;
    const [gid, tid] = c.target.split("/");
    const g = c.groups?.find((x) => x.id === gid)?.name ?? gid;
    return tid ? `${c.owner} in ${g}, topic ${tid}` : `${c.owner} in ${g}`;
  }
</script>

<section>
  <div class="heading">
    <h1>Channels</h1>
    {#if list?.managed_file && !editing}<button onclick={openNew}>Add a channel</button>{/if}
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="ok" role="status">{notice}</p>{/if}
  {#if restarting}
    <p class="ok" role="status">pao is restarting. <a href="/ui/">Sign in again</a> in a moment.</p>
  {:else if list?.restart_needed}
    <div class="card notice">
      <p>The channels on disk differ from the ones running. They apply when pao next starts.</p>
      <button class="secondary" onclick={restart}>Restart pao</button>
    </div>
  {/if}

  {#if list}
    {#if !list.managed_file}
      <p class="muted">
        pao manages no configuration file, so channels are shown here and changed in <code>pao.yaml</code>. To set them up here, add
        <code>managed: managed.yaml</code> to <code>pao.yaml</code> and start pao's service again.
      </p>
    {:else}
      <p class="muted">Channels you add here are kept in <code>{list.managed_file}</code>. Those in <code>pao.yaml</code> are changed there.</p>
    {/if}

    {#if list.channels.length === 0}
      <p class="muted">No channels yet. The local channel, this UI and the command line, is always there.</p>
    {:else}
      <table class="channels">
        <thead><tr><th>Channel</th><th>Provider</th><th class="wide-only">Conversation</th><th></th></tr></thead>
        <tbody>
          {#each list.channels as c (c.name)}
            <tr>
              <td>
                {c.name}
                <div class="muted small">id {c.id} · {c.managed ? "pao manages" : "pao.yaml"}{c.applied ? "" : " · applies at next start"}</div>
              </td>
              <td>{c.provider}</td>
              <td class="wide-only">{convLabel(c)}</td>
              <td class="row-actions">
                {#if c.managed}
                  <button class="link" disabled={busy} onclick={() => openEdit(c)}>Edit</button>
                  <button class="link danger" disabled={busy} onclick={() => remove(c)}>Remove</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}

    <h2>Providers</h2>
    <p class="muted small">
      A provider is a platform connection a channel is carried on, defined in <code>pao.yaml</code> only. It holds the credential, which this page
      never takes, shows or stores: it says only whether pao can read it.
    </p>
    {#if list.providers.length === 0}
      <p class="muted">No providers. Define one in <code>pao.yaml</code> to add a channel.</p>
    {:else}
      <table class="providers">
        <thead><tr><th>Provider</th><th>Credential</th><th class="wide-only">Channels</th></tr></thead>
        <tbody>
          {#each list.providers as p (p.name)}
            <tr>
              <td>{p.name}<div class="muted small">{p.kind}</div></td>
              <td>
                <span class="badge" class:warn={!p.credential.resolves}>{p.credential.resolves ? "set" : "not set"}</span>
                {#if p.credential.problem}<div class="muted small">{p.credential.problem}</div>{/if}
              </td>
              <td class="wide-only">{p.channels?.join(", ") || "—"}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}

    {#if editing}
      <h2>{editing === "new" ? "Add a channel" : `Change ${editing}`}</h2>
      <form class="stack" onsubmit={save}>
        <label for="c-name">Name</label>
        <input id="c-name" bind:value={name} required disabled={editing !== "new"} placeholder="lowercase letters, digits, - or _" />
        {#if editing !== "new"}<p class="muted small">Id {id}: it names the channel in every message id, and never changes.</p>{/if}
        <label for="c-provider">Provider</label>
        <select id="c-provider" bind:value={provider} required>
          {#each list.providers as p (p.name)}<option value={p.name}>{p.name} ({p.kind})</option>{/each}
        </select>

        <label for="c-owner">Owner</label>
        <input id="c-owner" bind:value={owner} required placeholder="the subject the channel belongs to" autocomplete="off" />

        <fieldset>
          <legend>Conversation</legend>
          <p class="muted small">
            The one conversation the channel is. pao acts only on what the owner writes there, and shows the channel's output there alone. Anything else
            is not acted on, and is kept in the record of rejected messages.
          </p>
          <label class="check"><input type="radio" bind:group={convWay} value="private" /> the owner's private conversation with the channel</label>
          <label class="check"><input type="radio" bind:group={convWay} value="group" /> a group, or a topic of a group</label>
          {#if convWay === "group"}
            <div class="group-row">
              <input aria-label="Group id" bind:value={groupId} placeholder="group id" required />
              <input aria-label="Group name" bind:value={groupName} placeholder="name" required />
              <input aria-label="Topic id" bind:value={topicId} placeholder="topic id (optional)" />
            </div>
          {/if}
        </fieldset>

        <div class="actions">
          <button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</button>
          <button type="button" class="secondary" onclick={() => (editing = "")}>Cancel</button>
        </div>
      </form>
    {/if}
  {/if}
</section>
