<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: DialogPrimitive.TitleProps = $props();
</script>

<DialogPrimitive.Title
  class={cn('text-[13px] font-semibold text-text-primary', className)}
  {...restProps}
/>
