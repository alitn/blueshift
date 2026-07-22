import { Dialog as DialogPrimitive } from 'bits-ui';

import Content from './DialogContent.svelte';
import Description from './DialogDescription.svelte';
import Overlay from './DialogOverlay.svelte';
import Title from './DialogTitle.svelte';

const Root = DialogPrimitive.Root;
const Trigger = DialogPrimitive.Trigger;
const Close = DialogPrimitive.Close;
const Portal = DialogPrimitive.Portal;

export {
  Root,
  Trigger,
  Close,
  Portal,
  Content,
  Overlay,
  Title,
  Description,
  //
  Root as Dialog,
  Trigger as DialogTrigger,
  Close as DialogClose,
  Content as DialogContent,
  Overlay as DialogOverlay,
  Title as DialogTitle,
  Description as DialogDescription
};
