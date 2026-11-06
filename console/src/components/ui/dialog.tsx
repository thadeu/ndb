import type { Child, FC } from 'hono/jsx';

export interface DialogProps {
  id: string;
  title: string;
  description?: string;
  open?: boolean;
  closeHref?: string;
  footer?: Child;
  children?: Child;
}

// openDialog gives a link or button the attribute that opens a dialog. The
// link keeps its href, so without the script it opens the page with the
// dialog already open.
export const openDialog = (id: string) => ({ 'data-dialog-open': id });

// Dialog is a native <dialog>. The server can render it open (the URL then
// shows it), and the script upgrades it to a modal.
export const Dialog: FC<DialogProps> = ({ id, title, description, open = false, closeHref, footer, children }) => (
  <dialog id={id} class="dialog" open={open} aria-labelledby={`${id}-title`}>
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
        ×
      </a>
    )}
  </dialog>
);

export function mount(): void {
  document.addEventListener('click', event => {
    const target = event.target;

    if (!(target instanceof Element)) return;

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
}
