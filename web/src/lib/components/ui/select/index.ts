import { Select as SelectPrimitive } from 'bits-ui';

import Content from './SelectContent.svelte';
import Item from './SelectItem.svelte';
import Trigger from './SelectTrigger.svelte';

const Root = SelectPrimitive.Root;
const Group = SelectPrimitive.Group;
const GroupHeading = SelectPrimitive.GroupHeading;

export {
  Root,
  Group,
  GroupHeading,
  Trigger,
  Content,
  Item,
  //
  Root as Select,
  Trigger as SelectTrigger,
  Content as SelectContent,
  Item as SelectItem,
  Group as SelectGroup,
  GroupHeading as SelectGroupHeading
};
