<script lang="ts">
  import AppShell from './AppShell.svelte';
</script>

<AppShell queue={2} storage="1.2 / 4.0 TB" engine="412" version="v9.9.9">
  {#snippet breadcrumb()}
    <span class="font-semibold text-text-primary">LIBRARY</span>
    <span class="text-text-faintest">▸</span>
    <span class="text-text-muted">Special Interviews</span>
  {/snippet}
  <div data-testid="shell-child">Main content region</div>
</AppShell>
