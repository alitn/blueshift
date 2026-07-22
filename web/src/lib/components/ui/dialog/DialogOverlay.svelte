<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: DialogPrimitive.OverlayProps = $props();
</script>

<DialogPrimitive.Overlay
  class={cn('animate-overlay-in fixed inset-0 z-50 bg-overlay-scrim', className)}
  {...restProps}
/>
