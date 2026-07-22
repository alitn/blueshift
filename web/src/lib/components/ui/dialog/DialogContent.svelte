<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';
  import DialogOverlay from './DialogOverlay.svelte';

  let {
    class: className,
    children,
    ...restProps
  }: DialogPrimitive.ContentProps & { children?: Snippet } = $props();
</script>

<DialogPrimitive.Portal>
  <DialogOverlay />
  <DialogPrimitive.Content
    class={cn(
      'animate-content-in fixed left-1/2 top-1/2 z-50 w-[440px] max-w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 rounded-4 border border-border-control bg-bg-4 p-3.5 text-text-primary outline-none',
      className
    )}
    {...restProps}
  >
    {@render children?.()}
  </DialogPrimitive.Content>
</DialogPrimitive.Portal>
