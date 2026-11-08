// A popover next to its trigger, in the native top layer (the popover
// attribute), so a dialog or a scroll box never cuts it. It goes below or
// above the trigger by the room there (popover-position.ts), follows scroll
// and resize, and closes on Escape or a click outside. On a phone it is a
// sheet on the bottom edge. One is open at a time.

import { place, type Placement } from './popover-position';

interface Open {
  pop: HTMLElement;
  trigger: HTMLElement;
  prefer: Placement;
  onClose?: () => void;
}

// Below this viewport width the popover is a sheet on the bottom edge.
const SHEET_BELOW = 520;

let current: Open | undefined;
let listening = false;

function reposition(): void {
  if (!current) return;

  const { pop, trigger, prefer } = current;

  pop.style.maxHeight = '';
  pop.dataset.placement = window.innerWidth < SHEET_BELOW ? 'sheet' : '';

  const placed = place(trigger.getBoundingClientRect(), { width: pop.offsetWidth, height: pop.offsetHeight }, { width: window.innerWidth, height: window.innerHeight }, { prefer, sheetBelow: SHEET_BELOW });

  pop.style.top = `${placed.top}px`;
  pop.style.left = `${placed.left}px`;
  pop.style.maxHeight = `${placed.maxHeight}px`;
  pop.dataset.placement = placed.placement;
}

function onClick(event: MouseEvent): void {
  const path = event.composedPath();

  if (current && !path.includes(current.pop) && !path.includes(current.trigger)) closePopover();
}

function onKey(event: KeyboardEvent): void {
  if (event.key !== 'Escape' || !current) return;

  const { trigger } = current;

  event.preventDefault();
  event.stopPropagation();
  closePopover();
  trigger.focus();
}

function listen(): void {
  if (listening) return;

  listening = true;
  document.addEventListener('click', onClick);
  document.addEventListener('keydown', onKey, true);
  window.addEventListener('resize', reposition);
  window.addEventListener('scroll', reposition, true);
}

// openPopover shows pop next to trigger and closes the one that is open.
export function openPopover(pop: HTMLElement, trigger: HTMLElement, options: { prefer?: Placement; onClose?: () => void } = {}): void {
  listen();
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

// repositionPopover places the open popover again, after its content
// changed size.
export function repositionPopover(): void {
  reposition();
}

// mountPopovers makes every [data-popover-open="id"] button open the element
// with that id.
export function mountPopovers(root: ParentNode = document): void {
  for (const trigger of root.querySelectorAll<HTMLElement>('[data-popover-open]')) {
    if (trigger.dataset.popoverMounted) continue;

    trigger.dataset.popoverMounted = '1';
    trigger.addEventListener('click', event => {
      const pop = document.getElementById(trigger.dataset.popoverOpen ?? '');

      if (!pop) return;

      event.preventDefault();

      if (popoverOpen(pop)) closePopover();
      else openPopover(pop, trigger);
    });
  }
}
