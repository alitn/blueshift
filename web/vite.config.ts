import { sveltekit } from '@sveltejs/kit/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig, type UserConfig } from 'vite';

// `test` is contributed by Vitest, which augments Vite's UserConfig at runtime.
// We assemble the config as a value and widen to UserConfig so svelte-check
// (which type-checks this file against Vite's own types) stays green without
// dragging Vitest's separately-bundled Vite types into the app type graph.
// In `make dev`, Vite serves the SPA with hot reload while the Go API runs on
// BS_API_PORT (default 8080). Proxying /api (and the local blob upload subtree)
// to the API keeps same-origin cookies working without CORS. This block only
// affects `vite dev`; the production build is embedded in the Go binary.
//
// Locally typed so this Node-run config needs no @types/node dependency.
declare const process: { env: Record<string, string | undefined> };
const apiPort = process.env.BS_API_PORT ?? '8080';
const apiTarget = `http://127.0.0.1:${apiPort}`;

const config = {
  plugins: [sveltekit(), svelteTesting()],
  server: {
    proxy: {
      '/api': { target: apiTarget, changeOrigin: false }
    }
  },
  resolve: {
    // bits-ui only publishes a `svelte` export condition; ensure the test
    // resolver honours it (alongside the browser build of Svelte deps).
    conditions: ['browser', 'svelte']
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest-setup.ts'],
    include: ['src/**/*.{test,spec}.{js,ts}'],
    // Component tests resolve the browser build of Svelte and load real CSS.
    css: true
  }
};

export default defineConfig(config as UserConfig);
