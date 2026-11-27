<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { node, type Agent, type WatchLine } from "../lib/api";
  import { watch } from "../lib/watch";

  let { agent: initial = "", onLost }: { agent?: string; onLost: (err: unknown) => void } = $props();

  // A long watch keeps only its latest lines on the page.
  const keep = 2000;
  const levels = [
    { n: 1, label: "1: messages" },
    { n: 2, label: "2: and tool calls" },
    { n: 3, label: "3: and tool results, reasoning" },
    { n: 4, label: "4: everything" },
  ];

  let agents: Agent[] = $state([]);
  // the page is made anew for each agent it opens on, so the first value is the one
  let agent = $state(untrack(() => initial));
  let level = $state(1);
  let lines: WatchLine[] = $state([]);
  let dropped = $state(0);
  let watching = $state(false);
  let error = $state("");
  let follow = $state(true);
  let pane: HTMLElement | undefined = $state();
  let abort: AbortController | null = null;

  onMount(() => {
    node()
      .then((info) => {
        agents = info.agents;
        if (!agent && agents.length) agent = agents[0].name;
        if (untrack(() => initial)) start();
      })
      .catch((err) => {
        onLost(err);
        error = err instanceof Error ? err.message : "Could not load the agents.";
      });
    return stop;
  });

  async function start() {
    stop();
    lines = [];
    dropped = 0;
    error = "";
    const ac = new AbortController();
    abort = ac;
    watching = true;
    try {
      await watch(agent, level, 0, add, ac.signal);
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "The watch ended.";
    } finally {
      if (abort === ac) {
        watching = false;
        abort = null;
      }
    }
  }

  function stop() {
    abort?.abort();
    abort = null;
    watching = false;
  }

  async function add(l: WatchLine) {
    lines.push(l);
    if (lines.length > keep) {
      dropped += lines.length - keep;
      lines.splice(0, lines.length - keep);
    }
    if (follow) {
      await tick();
      pane?.scrollTo({ top: pane.scrollHeight });
    }
  }

  // Scrolling up to read stops following; scrolling back to the end resumes.
  function scrolled() {
    if (pane) follow = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 24;
  }

  const at = (t?: string) => (t ? new Date(t).toLocaleTimeString() : "");
</script>

<section>
  <h1>Watch</h1>
  <p class="muted">An agent's output as it comes, at the level you pick. Watching changes nothing for the agent or anyone else.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="watch-controls">
    <select aria-label="Agent" bind:value={agent} disabled={watching}>
      {#each agents as a (a.name)}<option value={a.name}>{a.name}{a.state === "running" ? "" : " (stopped)"}</option>{/each}
    </select>
    <select aria-label="Level" bind:value={level} disabled={watching}>
      {#each levels as l (l.n)}<option value={l.n}>{l.label}</option>{/each}
    </select>
    {#if watching}
      <button class="secondary" onclick={stop}>Stop</button>
    {:else}
      <button onclick={start} disabled={!agent}>Watch</button>
    {/if}
  </div>
  <p class="muted small">Levels are the defaults; an agent's profile can map its output to levels of its own.</p>

  {#if watching || lines.length}
    <div class="watch" bind:this={pane} onscroll={scrolled} role="log" aria-live="polite">
      {#if dropped}<div class="muted small">{dropped} earlier lines are no longer shown.</div>{/if}
      {#each lines as l, i (i + dropped)}
        <div class="wline" class:history={l.history}><span class="muted small">{at(l.time)}</span> <span class="wtext">{l.text}</span></div>
      {/each}
      {#if watching && lines.length === 0}<div class="muted">Waiting for output…</div>{/if}
    </div>
    {#if !follow}<button class="link" onclick={() => { follow = true; pane?.scrollTo({ top: pane.scrollHeight }); }}>Follow the end</button>{/if}
  {/if}
</section>
