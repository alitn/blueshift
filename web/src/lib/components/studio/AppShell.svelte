<script lang="ts">
  import type { Snippet } from 'svelte';
  import StatusBar from './StatusBar.svelte';
  import TopBar from './TopBar.svelte';

  let {
    children,
    breadcrumb,
    renderState = 'IDLE',
    queue = 0,
    storage = '—',
    engine = '—',
    version = 'v0.0.0',
    onSignOut
  }: {
    children?: Snippet;
    breadcrumb?: Snippet;
    renderState?: string;
    queue?: number;
    storage?: string;
    engine?: string;
    version?: string;
    onSignOut?: () => void;
  } = $props();
</script>

<div class="flex h-screen flex-col overflow-hidden bg-bg-2 text-text-primary">
  <TopBar {breadcrumb} {renderState} {onSignOut} />
  <main class="min-h-0 flex-1 overflow-auto">
    {@render children?.()}
  </main>
  <StatusBar {queue} {storage} {engine} {version} />
</div>
