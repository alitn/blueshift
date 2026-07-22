<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';

  let {
    class: className,
    children,
    ...restProps
  }: DialogPrimitive.ContentProps & { children?: Snippet } = $props();
</script>

<DialogPrimitive.Portal>
  <DialogPrimitive.Overlay class="animate-overlay-in fixed inset-0 z-50 bg-overlay-scrim" />
  <DialogPrimitive.Content
    class={cn(
      'animate-drawer-in fixed inset-y-0 right-0 z-50 flex w-[404px] max-w-[calc(100vw-2rem)] flex-col border-l border-border-control bg-bg-3 text-text-primary outline-none',
      className
    )}
    {...restProps}
  >
    {@render children?.()}
  </DialogPrimitive.Content>
</DialogPrimitive.Portal>
