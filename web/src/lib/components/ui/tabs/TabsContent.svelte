<script lang="ts">
  import { Tabs as TabsPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  let { class: className, ...restProps }: TabsPrimitive.ContentProps = $props();
</script>

<TabsPrimitive.Content
  class={cn('pt-3 outline-none focus-visible:outline-none', className)}
  {...restProps}
/>
