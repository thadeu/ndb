import type { Child, FC } from 'hono/jsx';

import { place, type Placement } from './popover-position';

export interface PopoverProps {
  id: string;
  label: string;
  class?: string;
  children?: Child;
}

// popoverTrigger gives a button the attributes that open the popover id.
export const popoverTrigger = (id: string) => ({ 'data-popover-open': id, 'aria-haspopup': 'dialog', 'aria-expanded': 'false', 'aria-controls': id });

// Popover is a panel next to its trigger. It uses the native popover top
// layer, so a dialog or a scroll box never cuts it, and goes below or above
// the trigger by the room there (popover-position.ts).
export const Popover: FC<PopoverProps> = ({ id, label, class: extra, children }) => (
  <div id={id} class={extra ? `popover-layer ${extra}` : 'popover-layer'} popover="manual" role="dialog" aria-label={label}>
    {children}
  </div>
);

interface Open {
  pop: HTMLElement;
  trigger: HTMLElement;
  prefer: Placement;
  onClose?: () => void;
}

let current: Open | undefined;

function reposition(): void {
  if (!current) return;

  const { pop, trigger, prefer } = current;

  pop.style.maxHeight = '';

  const placed = place(trigger.getBoundingClientRect(), { width: pop.offsetWidth, height: pop.offsetHeight }, { width: window.innerWidth, height: window.innerHeight }, prefer);

  pop.style.top = `${placed.top}px`;
  pop.style.left = `${placed.left}px`;
  pop.style.maxHeight = `${placed.maxHeight}px`;
  pop.dataset.placement = placed.placement;
}

// openPopover shows pop next to trigger and closes the one that is open.
export function openPopover(pop: HTMLElement, trigger: HTMLElement, options: { prefer?: Placement; onClose?: () => void } = {}): void {
  closePopover();

  current = { pop, trigger, prefer: options.prefer ?? 'bottom-start', onClose: options.onClose };

  if (typeof pop.showPopover === 'function') pop.showPopover();
  else pop.hidden = false;

  pop.classList.add('open');
  trigger.setAttribute('aria-expanded', 'true');
  reposition();
}

export function closePopover(): void {
  if (!current) return;

  const { pop, trigger, onClose } = current;

  current = undefined;

  if (typeof pop.hidePopover === 'function' && pop.matches(':popover-open')) pop.hidePopover();

  pop.classList.remove('open');
  trigger.setAttribute('aria-expanded', 'false');
  onClose?.();
}

export function popoverOpen(pop: HTMLElement): boolean {
  return current?.pop === pop;
}

export function mount(): void {
  document.addEventListener('click', event => {
    const target = event.target;

    if (!(target instanceof Element)) return;

    const opener = target.closest<HTMLElement>('[data-popover-open]');

    if (opener) {
      const pop = document.getElementById(opener.dataset.popoverOpen ?? '');

      if (pop) {
        event.preventDefault();

        if (popoverOpen(pop)) closePopover();
        else openPopover(pop, opener);
      }

      return;
    }

    const path = event.composedPath();

    if (current && !path.includes(current.pop) && !path.includes(current.trigger)) closePopover();
  });

  document.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || !current) return;

    const { trigger } = current;

    event.preventDefault();
    event.stopPropagation();
    closePopover();
    trigger.focus();
  }, true);

  window.addEventListener('resize', reposition);
  window.addEventListener('scroll', reposition, true);
}
