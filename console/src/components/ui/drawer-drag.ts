// A bottom drawer has a grip at its top. Drag it up or down to change the
// height of the drawer; drag it far down, or flick it down, to close it.
// The height stays for the next open of the same drawer.
const MIN = 0.3;
const MAX = 0.95;
const KEY = 'ndb-ui:drawer-height:';

function store(dialog: HTMLDialogElement, ratio: number): void {
  try {
    localStorage.setItem(KEY + dialog.id, String(ratio));
  } catch {
    // Storage can be blocked. The height stays for this page view.
  }
}

function restore(dialog: HTMLDialogElement): void {
  try {
    const ratio = Number(localStorage.getItem(KEY + dialog.id));

    if (ratio >= MIN && ratio <= MAX) dialog.style.height = `${Math.round(ratio * 100)}dvh`;
  } catch {
    // Storage can be blocked. The drawer keeps its default height.
  }
}

function attach(dialog: HTMLDialogElement): void {
  if (dialog.querySelector(':scope > .drawer-grip')) return;

  const grip = document.createElement('div');

  grip.className = 'drawer-grip';
  grip.setAttribute('role', 'separator');
  grip.setAttribute('aria-label', 'Drag to change the height, or down to close');
  dialog.prepend(grip);
  dialog.classList.add('has-grip');

  restore(dialog);

  grip.addEventListener('pointerdown', start => {
    start.preventDefault();
    grip.setPointerCapture(start.pointerId);

    const startY = start.clientY;
    const startHeight = dialog.getBoundingClientRect().height;
    const view = window.innerHeight;
    const startTime = performance.now();

    let height = startHeight;

    dialog.classList.add('dragging');

    const move = (event: PointerEvent) => {
      height = Math.min(view * MAX, Math.max(80, startHeight - (event.clientY - startY)));
      dialog.style.height = `${height}px`;
    };

    const stop = (event: PointerEvent) => {
      grip.removeEventListener('pointermove', move);
      grip.removeEventListener('pointerup', stop);
      grip.removeEventListener('pointercancel', stop);
      dialog.classList.remove('dragging');

      const moved = event.clientY - startY;
      const flick = moved > 60 && performance.now() - startTime < 250;

      if (flick || height < view * MIN) {
        dialog.style.height = '';
        restore(dialog);
        dialog.close();

        return;
      }

      const ratio = height / view;

      dialog.style.height = `${Math.round(ratio * 100)}dvh`;
      store(dialog, ratio);
    };

    grip.addEventListener('pointermove', move);
    grip.addEventListener('pointerup', stop);
    grip.addEventListener('pointercancel', stop);
  });
}

export function mount(): void {
  for (const dialog of document.querySelectorAll<HTMLDialogElement>('dialog.drawer-bottom')) attach(dialog);
}
