<script lang="ts">
  import { DropdownMenu as DropdownMenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let {
    class: className,
    sideOffset = 4,
    ...restProps
  }: DropdownMenuPrimitive.ContentProps = $props();
</script>

<DropdownMenuPrimitive.Portal>
  <DropdownMenuPrimitive.Content
    {sideOffset}
    class={cn(
      'animate-content-in z-50 min-w-[180px] rounded-4 border border-border-control bg-bg-4 p-1 outline-none',
      className
    )}
    {...restProps}
  />
</DropdownMenuPrimitive.Portal>
