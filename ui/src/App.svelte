<script lang="ts">
  import { onMount } from "svelte";
  import { session, logout, Unauthorized } from "./lib/api";
  import Login from "./pages/Login.svelte";
  import Node from "./pages/Node.svelte";
  import Profiles from "./pages/Profiles.svelte";
  import ProfileEdit from "./pages/ProfileEdit.svelte";
  import NewProfile from "./pages/NewProfile.svelte";
  import Agents from "./pages/Agents.svelte";
  import Tasks from "./pages/Tasks.svelte";
  import TaskView from "./pages/TaskView.svelte";
  import NewTask from "./pages/NewTask.svelte";
  import Bindings from "./pages/Bindings.svelte";
  import Harvests from "./pages/Harvests.svelte";
  import HarvestView from "./pages/HarvestView.svelte";
  import Rejected from "./pages/Rejected.svelte";

  // checking: asking whether the cookie is still good; out: the login form;
  // in: the pages.
  let phase: "checking" | "out" | "in" = $state("checking");
  let page = $state(location.hash.replace(/^#\//, "") || "node");
  // draft is a new profile's text, from the new-profile form to the editor.
  let draft = $state("");

  onMount(() => {
    const onHash = () => {
      page = location.hash.replace(/^#\//, "") || "node";
      if (page !== "profiles/new") draft = "";
    };
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
      <a href="#/agents" class:active={page === "agents"}>Agents</a>
      <a href="#/tasks" class:active={page.startsWith("tasks")}>Tasks</a>
      <a href="#/bindings" class:active={page === "bindings"}>Bindings</a>
      <a href="#/harvests" class:active={page.startsWith("harvests")}>Harvests</a>
      <a href="#/rejected" class:active={page === "rejected"}>Rejected</a>
      <a href="#/profiles" class:active={page.startsWith("profiles")}>Profiles</a>
    </nav>
    <button class="link" onclick={signOut}>Sign out</button>
  </header>
  <main>
    {#if page === "node"}
      <Node onLost={lost} />
    {:else if page === "agents"}
      <Agents onLost={lost} />
    {:else if page === "harvests"}
      <Harvests onLost={lost} />
    {:else if page.startsWith("harvests/")}
      {#key page}<HarvestView id={decodeURIComponent(page.slice("harvests/".length))} onLost={lost} />{/key}
    {:else if page === "rejected"}
      <Rejected onLost={lost} />
    {:else if page === "bindings"}
      <Bindings onLost={lost} />
    {:else if page === "tasks"}
      <Tasks onLost={lost} />
    {:else if page === "tasks/new"}
      <NewTask onLost={lost} />
    {:else if page.startsWith("tasks/")}
      {#key page}<TaskView id={decodeURIComponent(page.slice("tasks/".length))} onLost={lost} />{/key}
    {:else if page === "profiles"}
      <Profiles onLost={lost} />
    {:else if page === "profiles/new"}
      {#if draft}
        {#key draft}<ProfileEdit file="" {draft} onLost={lost} />{/key}
      {:else}
        <NewProfile onDraft={(t) => (draft = t)} onLost={lost} />
      {/if}
    {:else if page.startsWith("profiles/")}
      {#key page}<ProfileEdit file={decodeURIComponent(page.slice("profiles/".length))} onLost={lost} />{/key}
    {:else}
      <p class="muted">No such page.</p>
    {/if}
  </main>
{/if}
