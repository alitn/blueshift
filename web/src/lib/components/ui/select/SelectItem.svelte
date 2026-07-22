<script lang="ts">
  import { Select as SelectPrimitive } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';

  // Capture the consumer snippet under a distinct name so it does not collide
  // with the `children` snippet bits-ui expects (which receives { selected }).
  let {
    class: className,
    children: content,
    ...restProps
  }: SelectPrimitive.ItemProps & { children?: Snippet } = $props();
</script>

<SelectPrimitive.Item
  class={cn(
    'flex cursor-pointer select-none items-center rounded-3 px-2.5 py-1.5 font-mono text-[10px] text-text-body outline-none transition-colors duration-hover ease-out data-[highlighted]:bg-accent-wash-12 data-[highlighted]:text-text-primary data-[selected]:text-text-primary',
    className
  )}
  {...restProps}
>
  {#snippet children({ selected })}
    <span class="flex-1">{@render content?.()}</span>
    {#if selected}
      <span aria-hidden="true" class="ml-auto text-accent-bright">•</span>
    {/if}
  {/snippet}
</SelectPrimitive.Item>
