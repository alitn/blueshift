<script lang="ts">
  import { Tooltip as TooltipPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let {
    class: className,
    sideOffset = 6,
    ...restProps
  }: TooltipPrimitive.ContentProps = $props();
</script>

<TooltipPrimitive.Portal>
  <TooltipPrimitive.Content
    {sideOffset}
    class={cn(
      'animate-content-in z-50 rounded-3 border border-border-strong bg-bg-4 px-2 py-1 font-mono text-[10.5px] uppercase tracking-wider text-text-primary',
      className
    )}
    {...restProps}
  />
</TooltipPrimitive.Portal>
