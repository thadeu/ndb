// The fold buttons of the sidebar and of the collection list. A button with
// data-collapse="side" or "list" switches that part between its full width
// and a rail, and keeps the choice in the browser. The layout reads the
// choices before the first paint.
export function mount(): void {
  // The connection menu closes on a click outside it, and on Esc.
  document.addEventListener('click', event => {
    const inside = event.target instanceof Element ? event.target.closest('.switcher-menu') : null;

    for (const menu of document.querySelectorAll<HTMLDetailsElement>('.switcher-menu[open]')) {
      if (menu !== inside) menu.open = false;
    }
  });

  document.addEventListener('keydown', event => {
    if (event.key !== 'Escape') return;

    for (const menu of document.querySelectorAll<HTMLDetailsElement>('.switcher-menu[open]')) menu.open = false;
  });

  document.addEventListener('click', event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-collapse]') : null;
    const part = button?.dataset.collapse;

    if (!part) return;

    const root = document.documentElement;
    const rail = root.dataset[part] !== 'rail';

    if (rail) root.dataset[part] = 'rail';
    else delete root.dataset[part];

    try {
      localStorage.setItem(`ndb-ui:${part}`, rail ? 'rail' : 'full');
    } catch {
      // Storage can be blocked. The part still folds for this page view.
    }
  });
}
