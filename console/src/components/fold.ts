// A <details data-fold="key"> remembers in the browser if the reader left
// it open or closed, so a new page load (such as a Run) does not change it.
// The server sets data-fold-force when the block must be open now, such as
// for an error in it.
const PREFIX = 'ndb-ui:fold:';

function stored(key: string): string | null {
  try {
    return localStorage.getItem(PREFIX + key);
  } catch {
    return null;
  }
}

function store(key: string, open: boolean): void {
  try {
    localStorage.setItem(PREFIX + key, open ? 'open' : 'closed');
  } catch {
    // Storage can be blocked. The block still opens and closes.
  }
}

export function mount(): void {
  for (const fold of document.querySelectorAll<HTMLDetailsElement>('details[data-fold]')) {
    const key = fold.dataset.fold ?? '';
    const choice = stored(key);

    if (choice && fold.dataset.foldForce === undefined) fold.open = choice === 'open';

    fold.querySelector(':scope > summary')?.addEventListener('click', () => {
      setTimeout(() => store(key, fold.open), 0);
    });
  }
}
