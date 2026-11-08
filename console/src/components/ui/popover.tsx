import type { Child, FC } from 'hono/jsx';

import { mountPopovers } from '@/lib/ui/popover';

export interface PopoverProps {
  id: string;
  label: string;
  class?: string;
  children?: Child;
}

// popoverTrigger gives a button the attributes that open the popover id.
export const popoverTrigger = (id: string) => ({ 'data-popover-open': id, 'aria-haspopup': 'dialog', 'aria-expanded': 'false', 'aria-controls': id });

// Popover is the popover of lib/ui: a panel next to its trigger, in the
// native top layer, below or above it by the room there.
export const Popover: FC<PopoverProps> = ({ id, label, class: extra, children }) => (
  <div id={id} class={extra ? `popover-layer ${extra}` : 'popover-layer'} popover="manual" role="dialog" aria-label={label}>
    {children}
  </div>
);

export function mount(): void {
  mountPopovers();
}
