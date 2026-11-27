<script lang="ts">
  import { login, APIError, Unauthorized } from "../lib/api";

  let { onSignedIn }: { onSignedIn: () => void } = $props();
  let token = $state("");
  let error = $state("");
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = "";
    busy = true;
    try {
      await login(token.trim());
      token = "";
      onSignedIn();
    } catch (err) {
      error = err instanceof Unauthorized ? "That token is not this node's." : err instanceof APIError ? err.message : "The node did not answer.";
    } finally {
      busy = false;
    }
  }
</script>

<main class="center">
  <form class="card login" onsubmit={submit}>
    <h1>pao</h1>
    <p class="muted">Sign in with this node's operator token.</p>
    <label for="token">Operator token</label>
    <input id="token" type="password" autocomplete="current-password" bind:value={token} required />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button type="submit" disabled={busy || !token.trim()}>{busy ? "Signing in…" : "Sign in"}</button>
  </form>
</main>
