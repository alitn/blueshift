import { DropdownMenu as DropdownMenuPrimitive } from 'bits-ui';

import Content from './DropdownMenuContent.svelte';
import Item from './DropdownMenuItem.svelte';
import Separator from './DropdownMenuSeparator.svelte';

const Root = DropdownMenuPrimitive.Root;
const Trigger = DropdownMenuPrimitive.Trigger;
const Group = DropdownMenuPrimitive.Group;
const GroupHeading = DropdownMenuPrimitive.GroupHeading;

export {
  Root,
  Trigger,
  Group,
  GroupHeading,
  Content,
  Item,
  Separator,
  //
  Root as DropdownMenu,
  Trigger as DropdownMenuTrigger,
  Content as DropdownMenuContent,
  Item as DropdownMenuItem,
  Separator as DropdownMenuSeparator,
  Group as DropdownMenuGroup
};
