<script lang="ts">
  import { DropdownMenu as DropdownMenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: DropdownMenuPrimitive.ItemProps = $props();
</script>

<DropdownMenuPrimitive.Item
  class={cn(
    'flex cursor-pointer select-none items-center gap-2 rounded-3 px-2.5 py-1.5 text-[11px] text-text-body outline-none transition-colors duration-hover ease-out data-[highlighted]:bg-accent-wash-12 data-[highlighted]:text-text-primary',
    className
  )}
  {...restProps}
/>
