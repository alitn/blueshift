import { sveltekit } from '@sveltejs/kit/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig, type UserConfig } from 'vite';

// `test` is contributed by Vitest, which augments Vite's UserConfig at runtime.
// We assemble the config as a value and widen to UserConfig so svelte-check
// (which type-checks this file against Vite's own types) stays green without
// dragging Vitest's separately-bundled Vite types into the app type graph.
const config = {
  plugins: [sveltekit(), svelteTesting()],
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
