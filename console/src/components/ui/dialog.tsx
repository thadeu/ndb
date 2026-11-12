import type { Child, FC } from 'hono/jsx';

import { Icon } from './icon';

export interface DialogProps {
  id: string;
  title: string;
  description?: string;
  open?: boolean;
  closeHref?: string;
  footer?: Child;
  class?: string;
  children?: Child;
}

// openDialog gives a link or button the attribute that opens a dialog. The
// link keeps its href, so without the script it opens the page with the
// dialog already open.
export const openDialog = (id: string) => ({ 'data-dialog-open': id });

// Dialog is a native <dialog>. The server can render it open (the URL then
// shows it), and the script upgrades it to a modal. A click outside closes
// it, and the page behind does not scroll while it is open (app.css).
export const Dialog: FC<DialogProps> = ({ id, title, description, open = false, closeHref, footer, class: extra, children }) => (
  <dialog id={id} class={extra ? `dialog ${extra}` : 'dialog'} open={open} aria-labelledby={`${id}-title`}>
    <div class="dialog-head">
      <h2 class="dialog-title" id={`${id}-title`}>
        {title}
      </h2>

      {description && <p class="dialog-desc">{description}</p>}
    </div>

    <div class="dialog-body">{children}</div>

    {footer && <div class="dialog-foot">{footer}</div>}

    {closeHref && (
      <a class="dialog-x" href={closeHref} data-dialog-close aria-label="Close">
        <Icon name="x" size={20} />
      </a>
    )}
  </dialog>
);

export function mount(): void {
  // A click closes a dialog only when the press also began on its backdrop.
  // A text selection that ends outside the dialog must not close it.
  let pressed: EventTarget | null = null;

  document.addEventListener('pointerdown', event => {
    const target = event.target;

    pressed = target instanceof HTMLDialogElement && outside(target, event) ? target : null;
  });

  document.addEventListener('click', event => {
    const target = event.target;

    if (!(target instanceof Element)) return;

    if (target instanceof HTMLDialogElement && target.classList.contains('dialog') && pressed === target && outside(target, event)) {
      target.close();

      return;
    }

    const opener = target.closest<HTMLElement>('[data-dialog-open]');
    const closer = target.closest<HTMLElement>('[data-dialog-close]');

    if (opener) {
      const dialog = document.getElementById(opener.dataset.dialogOpen ?? '');

      if (dialog instanceof HTMLDialogElement) {
        event.preventDefault();
        dialog.showModal();
      }

      return;
    }

    if (closer) {
      const dialog = closer.closest('dialog');

      if (dialog) {
        event.preventDefault();
        dialog.close();
      }
    }
  });

  for (const dialog of document.querySelectorAll<HTMLDialogElement>('dialog.dialog[open]')) {
    dialog.close();
    dialog.showModal();
  }

  for (const dialog of document.querySelectorAll<HTMLDialogElement>('dialog.dialog')) {
    dialog.addEventListener('close', () => forget(dialog));
  }
}

// outside tells if a click on the dialog element was on its backdrop. The
// backdrop belongs to the dialog, so only the position can tell.
function outside(dialog: HTMLDialogElement, event: MouseEvent): boolean {
  const box = dialog.getBoundingClientRect();

  return event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom;
}

// forget takes the dialog out of the URL after it closes, so a reload does
// not open it again. The close link has the URL of the page without it.
function forget(dialog: HTMLDialogElement): void {
  const link = dialog.querySelector<HTMLAnchorElement>(':scope > .dialog-x');

  if (link && link.href !== location.href) history.replaceState(null, '', link.href);
}
