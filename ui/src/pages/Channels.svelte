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
  let kind = $state("");
  let credWay = $state<"secret" | "env">("secret");
  let credSecret = $state("");
  let credEnv = $state("");
  let optionsText = $state("{}");
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
    kind = list.kinds[0] ?? "";
    credWay = list.secrets.length ? "secret" : "env";
    credSecret = list.secrets[0] ?? "";
    credEnv = "";
    optionsText = "{}";
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
    kind = c.kind;
    credWay = c.credential_secret ? "secret" : "env";
    credSecret = c.credential_secret ?? list?.secrets[0] ?? "";
    credEnv = c.credential_env ?? "";
    optionsText = JSON.stringify(c.options ?? {}, null, 2);
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
    let options: Record<string, unknown>;
    try {
      const parsed = JSON.parse(optionsText || "{}");
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error();
      options = parsed;
    } catch {
      error = "Options must be a JSON object, such as {}.";
      return;
    }
    const c: ChannelConfig = {
      id,
      name: name.trim(),
      kind,
      credential_secret: credWay === "secret" ? credSecret : undefined,
      credential_env: credWay === "env" ? credEnv.trim() : undefined,
      options,
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

  const credLabel = (c: ChannelConfig) => (c.credential_secret ? `secret ${c.credential_secret}` : `variable ${c.credential_env}`);
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
        <thead><tr><th>Channel</th><th>Credential</th><th class="wide-only">Conversation</th><th></th></tr></thead>
        <tbody>
          {#each list.channels as c (c.name)}
            <tr>
              <td>
                {c.name}
                <div class="muted small">{c.kind} · id {c.id} · {c.managed ? "pao manages" : "pao.yaml"}{c.applied ? "" : " · applies at next start"}</div>
              </td>
              <td>
                {credLabel(c)}
                <div><span class="badge" class:warn={!c.credential.resolves}>{c.credential.resolves ? "set" : "not set"}</span></div>
                {#if c.credential.problem}<div class="muted small">{c.credential.problem}</div>{/if}
              </td>
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

    {#if editing}
      <h2>{editing === "new" ? "Add a channel" : `Change ${editing}`}</h2>
      <form class="stack" onsubmit={save}>
        <label for="c-name">Name</label>
        <input id="c-name" bind:value={name} required disabled={editing !== "new"} placeholder="lowercase letters, digits, - or _" />
        {#if editing !== "new"}<p class="muted small">Id {id}: it names the channel in every message id, and never changes.</p>{/if}
        <label for="c-kind">Kind</label>
        <select id="c-kind" bind:value={kind}>
          {#each list.kinds as k (k)}<option value={k}>{k}</option>{/each}
        </select>

        <fieldset>
          <legend>Credential</legend>
          <p class="muted small">pao reads the credential from where you name it here. Its value is never entered, shown or stored by this page: put it there yourself.</p>
          <label class="check"><input type="radio" bind:group={credWay} value="secret" disabled={list.secrets.length === 0} /> one of the node's secrets</label>
          {#if credWay === "secret"}
            <select aria-label="Secret" bind:value={credSecret}>
              {#each list.secrets as s (s)}<option value={s}>{s}</option>{/each}
            </select>
          {/if}
          <label class="check"><input type="radio" bind:group={credWay} value="env" /> a variable in pao's environment</label>
          {#if credWay === "env"}
            <input aria-label="Variable" bind:value={credEnv} placeholder="PAO_CHAT_TOKEN" required autocomplete="off" />
          {/if}
          {#if list.secrets.length === 0}<p class="muted small">The node has no secrets a channel can name.</p>{/if}
        </fieldset>

        <label for="c-options">Options <span class="muted small">— the adapter's own, as JSON</span></label>
        <textarea id="c-options" class="code" rows="4" bind:value={optionsText}></textarea>

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
