<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  let {
    class: className,
    children,
    ...restProps
  }: HTMLAttributes<HTMLDivElement> & { children?: Snippet } = $props();
</script>

<!-- Drawer header is a fixed 48px band per DESIGN.md Drawer rules. -->
<div
  class={cn(
    'flex h-[48px] flex-none items-center gap-3 border-b border-border-subtle px-4',
    className
  )}
  {...restProps}
>
  {@render children?.()}
</div>
