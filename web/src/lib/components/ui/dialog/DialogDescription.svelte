<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: DialogPrimitive.DescriptionProps = $props();
</script>

<DialogPrimitive.Description
  class={cn('mt-1.5 text-[11px] leading-snug text-text-muted', className)}
  {...restProps}
/>
