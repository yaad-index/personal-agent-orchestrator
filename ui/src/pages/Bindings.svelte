<script lang="ts">
  import { onMount } from "svelte";
  import {
    node,
    channels,
    bindings,
    bind,
    unbind,
    channelDefaults,
    setDefault,
    clearDefault,
    type Agent,
    type Binding,
    type Channel,
  } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let agents: Agent[] = $state([]);
  let chans: Channel[] = $state([]);
  let list: Binding[] = $state([]);
  let defaults: Record<string, string> = $state({});
  let loaded = $state(false);
  let busy = $state(false);
  let error = $state("");
  let notice = $state("");

  // the new binding
  let agent = $state("");
  let stream = $state("stdout");
  let dest = $state("channel:local");
  let privateTo = $state("");
  let level = $state("");
  let prefix = $state("");

  // A destination is the local channel, an admitted group or named topic, or a
  // private conversation with a subject.
  const groupDests = $derived(chans.flatMap((c) => c.groups.map((g) => ({ to: `group:${g}`, label: `${g} (${c.name})` }))));
  // A default is set on a configured channel that takes chat, or a group.
  const defaultKeys = $derived([
    ...chans.filter((c) => c.kind !== "local").map((c) => ({ key: c.name, label: `${c.name} (${c.kind})` })),
    ...groupDests.map((g) => ({ key: g.to, label: g.label })),
  ]);

  async function load() {
    try {
      const [info, cs, bs, ds] = await Promise.all([node(), channels(), bindings(), channelDefaults()]);
      agents = info.agents;
      chans = cs;
      list = bs.bindings;
      defaults = Object.fromEntries(ds.defaults.map((d) => [d.channel ?? "", d.agent]));
      if (!agent && agents.length) agent = agents[0].name;
      loaded = true;
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the bindings.";
    }
  }

  onMount(load);

  async function run(what: () => Promise<string>) {
    busy = true;
    error = "";
    notice = "";
    try {
      notice = await what();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "That did not work.";
    } finally {
      busy = false;
      await load();
    }
  }

  function add(e: SubmitEvent) {
    e.preventDefault();
    const to = dest === "private" ? `private:${privateTo.trim()}` : dest;
    run(async () => {
      const b = await bind({ agent, stream, to, level: stream === "stdout" ? level.trim() || undefined : undefined, prefix: stream === "stdout" ? prefix.trim() || undefined : undefined });
      prefix = "";
      return `${b.agent}'s ${b.stream || "stdout"} now goes to ${b.to}.`;
    });
  }

  function remove(b: Binding) {
    if (!confirm(`Stop sending ${b.agent}'s ${b.stream || "stdout"} to ${b.to}?`)) return;
    run(async () => {
      await unbind(b);
      return `${b.agent}'s ${b.stream || "stdout"} no longer goes to ${b.to}.`;
    });
  }

  const pick = (key: string, value: string) =>
    run(async () => {
      if (!value) {
        const c = await clearDefault(key);
        return c.previous ? `${key} has no default agent now (was ${c.previous}).` : `${key} has no default agent.`;
      }
      const c = await setDefault(key, value);
      return `${key}'s default agent is ${value}${c.previous ? `, was ${c.previous}` : ""}${c.left ? `; ${value} is no longer the default of ${c.left}` : ""}.`;
    });
</script>

<section>
  <h1>Bindings</h1>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="ok" role="status">{notice}</p>{/if}

  {#if loaded}
    <h2>Where agents' output goes</h2>
    {#if list.length === 0}
      <p class="muted">No standing bindings.</p>
    {:else}
      <table class="bindings">
        <thead><tr><th>Agent</th><th>Goes to</th><th class="wide-only">Level</th><th></th></tr></thead>
        <tbody>
          {#each list as b (b.agent + (b.stream ?? "") + b.to)}
            <tr>
              <td>{b.agent}<div class="muted small">{b.stream || "stdout"}{b.prefix ? ` · as ${b.prefix}` : ""}</div></td>
              <td>{b.to}</td>
              <td class="wide-only">{b.level || "—"}</td>
              <td class="row-actions">
                {#if b.from === "config"}
                  <span class="muted small" title="Set in the configuration file">config</span>
                {:else}
                  <button class="link danger" disabled={busy} onclick={() => remove(b)}>Remove</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}

    <h2>Bind an agent's output</h2>
    {#if agents.length === 0}
      <p class="muted">No agents to bind.</p>
    {:else}
      <form class="stack" onsubmit={add}>
        <label for="b-agent">Agent</label>
        <select id="b-agent" bind:value={agent}>
          {#each agents as a (a.name)}<option value={a.name}>{a.name}</option>{/each}
        </select>
        <label for="b-stream">Output</label>
        <select id="b-stream" bind:value={stream}>
          <option value="stdout">its replies (stdout)</option>
          <option value="stderr">its errors (stderr)</option>
        </select>
        <label for="b-dest">Goes to</label>
        <select id="b-dest" bind:value={dest}>
          <option value="channel:local">the local channel</option>
          {#each groupDests as g (g.to)}<option value={g.to}>group {g.label}</option>{/each}
          <option value="private">a private conversation…</option>
        </select>
        {#if dest === "private"}
          <label for="b-private">With: a registered person by subject name, optionally @channel</label>
          <input id="b-private" bind:value={privateTo} placeholder="alice or alice@telegram" required />
        {/if}
        {#if stream === "stdout"}
          <label for="b-level">Level (optional): a number, or "log"</label>
          <input id="b-level" bind:value={level} placeholder="the node's own" />
          <label for="b-prefix">Shown as (optional): in place of the agent's name</label>
          <input id="b-prefix" bind:value={prefix} />
        {/if}
        <button type="submit" disabled={busy || !agent}>Bind</button>
      </form>
    {/if}

    <h2>Default agent of each channel</h2>
    <p class="muted">A message on a channel or group that names no agent goes to its default.</p>
    {#if defaultKeys.length === 0}
      <p class="muted">No configured channels take chat on this node.</p>
    {:else}
      <table class="defaults">
        <thead><tr><th>Channel or group</th><th>Default agent</th></tr></thead>
        <tbody>
          {#each defaultKeys as d (d.key)}
            <tr>
              <td>{d.label}</td>
              <td>
                <select aria-label={`Default agent of ${d.key}`} value={defaults[d.key] ?? ""} disabled={busy} onchange={(e) => pick(d.key, (e.currentTarget as HTMLSelectElement).value)}>
                  <option value="">none</option>
                  {#each agents as a (a.name)}<option value={a.name}>{a.name}</option>{/each}
                  {#if defaults[d.key] && !agents.some((a) => a.name === defaults[d.key])}<option value={defaults[d.key]}>{defaults[d.key]}</option>{/if}
                </select>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>
