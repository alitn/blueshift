<script lang="ts">
  import { Select as SelectPrimitive } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';

  let {
    class: className,
    sideOffset = 4,
    children,
    ...restProps
  }: SelectPrimitive.ContentProps & { children?: Snippet } = $props();
</script>

<SelectPrimitive.Portal>
  <SelectPrimitive.Content
    {sideOffset}
    class={cn(
      'animate-content-in z-50 max-h-64 min-w-[var(--bits-select-anchor-width)] overflow-y-auto rounded-4 border border-border-control bg-bg-4 p-1',
      className
    )}
    {...restProps}
  >
    <SelectPrimitive.Viewport>
      {@render children?.()}
    </SelectPrimitive.Viewport>
  </SelectPrimitive.Content>
</SelectPrimitive.Portal>
