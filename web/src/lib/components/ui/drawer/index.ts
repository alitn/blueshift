import { Dialog as DialogPrimitive } from 'bits-ui';

import Content from './DrawerContent.svelte';
import Header from './DrawerHeader.svelte';

// The drawer is a right slide-over built on the dialog primitive (bits-ui ships
// no dedicated drawer). Same focus trap and scrim; different geometry.
const Root = DialogPrimitive.Root;
const Trigger = DialogPrimitive.Trigger;
const Close = DialogPrimitive.Close;
const Title = DialogPrimitive.Title;

export {
  Root,
  Trigger,
  Close,
  Title,
  Content,
  Header,
  //
  Root as Drawer,
  Trigger as DrawerTrigger,
  Close as DrawerClose,
  Title as DrawerTitle,
  Content as DrawerContent,
  Header as DrawerHeader
};
