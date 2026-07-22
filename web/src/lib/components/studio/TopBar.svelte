<script lang="ts">
  import type { Snippet } from 'svelte';
  import {
    DropdownMenu,
    DropdownMenuTrigger,
    DropdownMenuContent,
    DropdownMenuItem
  } from '$lib/components/ui/dropdown-menu';

  // RENDER indicator placeholder. Only IDLE is wired in M0; the active state
  // (accent-border + accent-wash-12 fill) lands with the render pipeline.
  let {
    breadcrumb,
    renderState = 'IDLE',
    initials = 'MK',
    onSignOut
  }: {
    breadcrumb?: Snippet;
    renderState?: string;
    initials?: string;
    onSignOut?: () => void;
  } = $props();
</script>

<header
  class="flex h-topbar flex-none items-center gap-4 border-b border-border-subtle bg-bg-3 px-5"
>
  <!-- Wordmark: 2×15px accent tick + BLUE SHIFT 13.5/700 + STUDIO 9/500 @0.26em -->
  <div class="flex items-center gap-2.5">
    <div class="h-[15px] w-[2px] flex-none bg-accent"></div>
    <div class="flex items-baseline gap-1.5">
      <span class="text-[13.5px] font-bold leading-none tracking-[-0.01em] text-text-primary"
        >BLUE SHIFT</span
      >
      <span class="text-[9px] font-medium leading-none tracking-[0.26em] text-text-muted"
        >STUDIO</span
      >
    </div>
  </div>

  <div class="h-4 w-px flex-none bg-border-subtle"></div>

  <!-- Breadcrumb slot -->
  <nav class="flex items-center gap-2 text-[11px]" aria-label="Breadcrumb">
    {#if breadcrumb}
      {@render breadcrumb()}
    {:else}
      <span class="font-semibold tracking-[0.06em] text-text-primary">LIBRARY</span>
    {/if}
  </nav>

  <div class="flex-1"></div>

  <!-- RENDER indicator chip (placeholder) -->
  <div
    class="flex items-center gap-2 rounded-3 border border-border-strong px-2.5 py-1"
    aria-label="Render status"
  >
    <span class="font-mono text-[9px] tracking-[0.14em] text-text-muted">RENDER</span>
    <span class="font-mono text-[9px] text-text-faint">{renderState}</span>
  </div>

  <!-- Settings gear -->
  <button
    type="button"
    class="flex h-[15px] w-[15px] flex-none items-center justify-center text-text-muted transition-colors duration-hover ease-out hover:text-text-primary"
    aria-label="Settings"
  >
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.2" />
      <path
        d="M8 1.2v2.4M8 12.4v2.4M1.2 8h2.4M12.4 8h2.4M3.2 3.2l1.7 1.7M11.1 11.1l1.7 1.7M12.8 3.2l-1.7 1.7M4.9 11.1l-1.7 1.7"
        stroke="currentColor"
        stroke-width="1.1"
      />
    </svg>
  </button>

  <!-- Avatar → account dropdown (vendored dropdown-menu primitive) -->
  <DropdownMenu>
    <DropdownMenuTrigger
      aria-label="Account menu"
      class="flex h-6 w-6 flex-none items-center justify-center rounded-full border border-border-strong bg-bg-4 font-mono text-[8.5px] text-text-muted outline-none transition-colors duration-hover ease-out hover:border-border-hover data-[state=open]:border-accent-border"
    >
      {initials}
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end">
      <DropdownMenuItem onSelect={() => onSignOut?.()}>Sign out</DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</header>
