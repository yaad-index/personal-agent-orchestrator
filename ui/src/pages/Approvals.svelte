<script lang="ts">
  import { onMount } from "svelte";
  import { questions, decide, type Question } from "../lib/api";

  let { onLost }: { onLost: (err: unknown) => void } = $props();
  let list: Question[] | null = $state(null);
  let reasons: Record<string, string> = $state({});
  let busy = $state("");
  let error = $state("");

  async function load() {
    try {
      list = await questions();
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not load the questions.";
    }
  }

  onMount(() => {
    load();
    // a question is asked while the page is open; look again every few seconds
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  });

  // The question's text ends with how to answer it in chat; here the buttons
  // below are the answer.
  const asked = (text: string) =>
    text
      .split("\n")
      .filter((l) => !l.startsWith("answer:"))
      .join("\n");

  async function answer(q: Question, decision: "allow" | "always" | "deny") {
    busy = q.id;
    error = "";
    try {
      await decide(q.id, decision, decision === "deny" ? (reasons[q.id] || "").trim() : "");
    } catch (err) {
      onLost(err);
      error = err instanceof Error ? err.message : "Could not answer.";
    } finally {
      busy = "";
      await load();
    }
  }
</script>

<section>
  <h1>Approvals</h1>
  <p class="muted">An agent's action the gate asks you about waits here until you answer, or until it times out.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if list && list.length === 0}
    <p class="muted">Nothing is waiting.</p>
  {:else if list}
    {#each list as q (q.id)}
      <div class="card question">
        <div class="heading">
          <strong>{q.agent} · {q.tool}</strong>
          <span class="muted">{q.id} · {new Date(q.asked).toLocaleTimeString()}</span>
        </div>
        <pre>{asked(q.text)}</pre>
        <div class="actions">
          <button disabled={!!busy} onclick={() => answer(q, "allow")}>Allow</button>
          <button class="secondary" disabled={!!busy} onclick={() => answer(q, "always")}>Always</button>
          <input aria-label="Reason to deny" placeholder="reason (optional)" bind:value={reasons[q.id]} />
          <button class="secondary danger" disabled={!!busy} onclick={() => answer(q, "deny")}>Deny</button>
        </div>
      </div>
    {/each}
  {/if}
</section>
