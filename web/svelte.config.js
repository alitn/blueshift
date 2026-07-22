import adapter from "@sveltejs/adapter-static";
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // The vendored ui/ wrappers forward rest props to the underlying bits-ui
    // primitive — the idiomatic pattern. We never compile to custom elements,
    // so the custom-element rest-prop warning is a false positive here. All
    // other compiler warnings (a11y, etc.) still surface.
    warningFilter: (warning) => warning.code !== 'custom_element_props_identifier'
  },
  kit: {
    // SPA: the Go embed serves a single index.html and lets the client router
    // resolve deep links. No SSR, no per-route prerender.
    adapter: adapter({
      fallback: "index.html",
      strict: false,
    }),
    alias: {
      $lib: "src/lib",
    },
  },
};

export default config;
