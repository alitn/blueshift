import { Tabs as TabsPrimitive } from 'bits-ui';

import Content from './TabsContent.svelte';
import List from './TabsList.svelte';
import Trigger from './TabsTrigger.svelte';

const Root = TabsPrimitive.Root;

export {
  Root,
  List,
  Trigger,
  Content,
  //
  Root as Tabs,
  List as TabsList,
  Trigger as TabsTrigger,
  Content as TabsContent
};
