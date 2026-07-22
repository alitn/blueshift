<script lang="ts">
  import { Select as SelectPrimitive } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';

  let {
    class: className,
    children,
    ...restProps
  }: SelectPrimitive.TriggerProps & { children?: Snippet } = $props();
</script>

<SelectPrimitive.Trigger
  class={cn(
    'flex h-8 items-center justify-between gap-2 rounded-3 border border-border-strong bg-transparent px-2.5 font-mono text-[10px] text-text-primary outline-none transition-colors duration-hover ease-out hover:border-border-hover focus-visible:border-accent-border data-[state=open]:border-accent-border',
    className
  )}
  {...restProps}
>
  {@render children?.()}
  <span aria-hidden="true" class="text-text-faint">▾</span>
</SelectPrimitive.Trigger>
