import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The build is embedded in the pao binary and served at /ui on the node's
// localhost listener. The output is committed, so building pao needs no Node.
export default defineConfig({
  plugins: [svelte()],
  base: "/ui/",
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // No inline scripts or styles: the page's content security policy allows
    // only files from pao itself.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
  },
  server: {
    // During development, the API calls go to a running node.
    proxy: { "/v1": "http://127.0.0.1:7744", "/ui/api": "http://127.0.0.1:7744" },
  },
});
