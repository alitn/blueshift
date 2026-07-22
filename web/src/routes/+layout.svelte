<script lang="ts">
  import type { Snippet } from 'svelte';
  import '../app.css';
  import AppShell from '$lib/components/studio/AppShell.svelte';
  import { Toaster } from '$lib/components/ui/toast';

  let { children }: { children?: Snippet } = $props();
</script>

<AppShell>
  {@render children?.()}
</AppShell>

<Toaster />
