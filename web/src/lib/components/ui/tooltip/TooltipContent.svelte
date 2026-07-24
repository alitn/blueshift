<script lang="ts">
  import { Tooltip as TooltipPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let {
    class: className,
    sideOffset = 6,
    variant = 'label',
    ...restProps
  }: TooltipPrimitive.ContentProps & {
    /**
     * label — the default micro tooltip (mono uppercase chip).
     * card  — DESIGN.md popover conventions (bg-4 surface, border-control,
     *         radius 5px, padding 14px) for rich hover cards like the
     *         Library's pipeline details. Both are token-only.
     */
    variant?: 'label' | 'card';
  } = $props();
</script>

<TooltipPrimitive.Portal>
  <TooltipPrimitive.Content
    {sideOffset}
    class={cn(
      'animate-content-in z-50',
      variant === 'label' &&
        'rounded-3 border border-border-strong bg-bg-4 px-2 py-1 font-mono text-[10.5px] uppercase tracking-wider text-text-primary',
      variant === 'card' && 'rounded-4 border border-border-control bg-bg-4 p-3.5',
      className
    )}
    {...restProps}
  />
</TooltipPrimitive.Portal>
