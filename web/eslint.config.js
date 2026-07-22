import js from "@eslint/js";
import svelte from "eslint-plugin-svelte";
import globals from "globals";
import ts from "typescript-eslint";
import svelteConfig from "./svelte.config.js";

export default ts.config(
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs["flat/recommended"],
  {
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
      },
    },
  },
  {
    files: ["**/*.svelte", "**/*.svelte.ts", "**/*.svelte.js"],
    languageOptions: {
      parserOptions: {
        projectService: true,
        extraFileExtensions: [".svelte"],
        parser: ts.parser,
        svelteConfig,
      },
    },
    rules: {
      // Compiler *errors* still fail the lint; warnings are enforced by
      // `svelte-check --fail-on-warnings` (which honours svelte.config's
      // warningFilter), so we don't double-report them here. This avoids the
      // custom-element rest-prop false positive on the vendored ui wrappers.
      "svelte/valid-compile": ["error", { ignoreWarnings: true }],
    },
  },
  {
    ignores: ["build/", ".svelte-kit/", "dist/", "node_modules/"],
  },
);
