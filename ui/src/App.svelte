<script lang="ts">
  import { onMount } from "svelte";
  import { session, logout, Unauthorized } from "./lib/api";
  import Login from "./pages/Login.svelte";
  import Node from "./pages/Node.svelte";

  // checking: asking whether the cookie is still good; out: the login form;
  // in: the pages.
  let phase: "checking" | "out" | "in" = $state("checking");
  let page = $state(location.hash.replace(/^#\//, "") || "node");

  onMount(() => {
    const onHash = () => (page = location.hash.replace(/^#\//, "") || "node");
    window.addEventListener("hashchange", onHash);
    session()
      .then(() => (phase = "in"))
      .catch(() => (phase = "out"));
    return () => window.removeEventListener("hashchange", onHash);
  });

  async function signOut() {
    try {
      await logout();
    } finally {
      phase = "out";
    }
  }

  // A page that finds the session gone sends the user back to sign in.
  function lost(err: unknown) {
    if (err instanceof Unauthorized) phase = "out";
  }
</script>

{#if phase === "checking"}
  <main class="center"><p class="muted">Loading…</p></main>
{:else if phase === "out"}
  <Login onSignedIn={() => (phase = "in")} />
{:else}
  <header class="bar">
    <span class="brand">pao</span>
    <nav>
      <a href="#/node" class:active={page === "node"}>Node</a>
    </nav>
    <button class="link" onclick={signOut}>Sign out</button>
  </header>
  <main>
    {#if page === "node"}
      <Node onLost={lost} />
    {:else}
      <p class="muted">No such page.</p>
    {/if}
  </main>
{/if}
