// The script of the document drawer of the Documents tab. A click on a row
// of the table opens its document in the bottom drawer without a page load:
// it reads the page of the row link, takes the content of the drawer from
// it, and puts the link in the URL. The dialog script closes the drawer and
// takes the document out of the URL. Without this script, the _id link
// opens the same page with the drawer already open.
const DRAWER = 'doc';
const ROW = '[data-doc-href]';
const PANE = '.doc-pane';
const LIST = '.doc-list';
const ROW_ON = 'find-row-on';
const CONTROLS = 'a, button, input, select, textarea, label, summary';

// DOC_READY tells the editor island that the drawer has a new document.
export const DOC_READY = 'ndb:doc-ready';

// DOC_SAVED tells this script that the editor saved the open document, so
// the table and the list of the drawer show its new values.
export const DOC_SAVED = 'ndb:doc-saved';

// rowTarget gives the row that a click opens, or null when the click was on
// a control of the row (its checkbox, a link, a button) or on a cell with a
// checkbox. The _id link is the exception: it opens the document.
export function rowTarget(target: Element): HTMLElement | null {
  const row = target.closest<HTMLElement>(ROW);

  if (!row) return null;

  const control = target.closest(CONTROLS);
  const own = control !== null && row.contains(control);

  if (own && !control.matches('a[data-doc-link]')) return null;

  if (target.closest('.dtable-check')) return null;

  return row;
}

function plainClick(event: MouseEvent): boolean {
  return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}

// mark shows which document is open, in the table and in the list of the
// drawer. Both have one entry for each document, with the same link.
function mark(href: string | null): void {
  for (const entry of document.querySelectorAll<HTMLElement>(ROW)) {
    const on = href !== null && entry.dataset.docHref === href;

    entry.classList.toggle(ROW_ON, on);

    if (entry.matches('a')) entry.toggleAttribute('aria-current', on);
  }
}

// show reads the page of the document and moves the content of its drawer
// into the drawer of this page. Any failure becomes a normal navigation.
async function show(drawer: HTMLDialogElement, row: HTMLElement, href: string): Promise<void> {
  const body = drawer.querySelector(PANE);

  mark(row.dataset.docHref ?? null);
  drawer.setAttribute('aria-busy', 'true');

  try {
    const response = await fetch(href, { headers: { Accept: 'text/html' } });
    const page = new DOMParser().parseFromString(await response.text(), 'text/html');
    const next = page.querySelector(`#${DRAWER} ${PANE}`);

    if (!response.ok || !body || !next || next.childElementCount === 0) throw new Error('no document in the answer');

    body.replaceChildren(...Array.from(next.childNodes).map(node => document.importNode(node, true)));
  } catch {
    window.location.assign(href);

    return;
  } finally {
    drawer.removeAttribute('aria-busy');
  }

  history.replaceState(null, '', href);

  if (!drawer.open) drawer.showModal();

  if (!drawer.querySelector(LIST)?.contains(document.activeElement)) drawer.querySelector<HTMLElement>(`${LIST} .${ROW_ON}`)?.focus({ preventScroll: true });

  body?.scrollTo(0, 0);
  document.dispatchEvent(new Event(DOC_READY));
}

// refresh reads this page again and puts the new rows of the table and of
// the list of the drawer in place, as a morph: the drawer, the editor and
// the scroll do not move. A failure leaves the old rows; they are only stale.
async function refresh(): Promise<void> {
  try {
    const response = await fetch(window.location.href, { headers: { Accept: 'text/html' } });

    if (!response.ok) return;

    const page = new DOMParser().parseFromString(await response.text(), 'text/html');
    const open = document.querySelector<HTMLElement>(`${ROW}.${ROW_ON}`)?.dataset.docHref ?? null;

    for (const selector of ['.dtable tbody', `#${DRAWER} .doc-items`]) {
      const now = document.querySelector(selector);
      const next = page.querySelector(selector);

      if (now && next) now.replaceChildren(...Array.from(next.childNodes).map(node => document.importNode(node, true)));
    }

    mark(open);
  } catch {
    // The rows stay as they were.
  }
}

export function mount(): void {
  const drawer = document.getElementById(DRAWER);

  if (!(drawer instanceof HTMLDialogElement) || !drawer.classList.contains('doc-drawer')) return;

  document.addEventListener('click', event => {
    const target = event.target;

    if (!(target instanceof Element) || !plainClick(event) || event.defaultPrevented) return;

    const row = rowTarget(target);
    const selecting = (window.getSelection()?.toString() ?? '') !== '';

    if (!row || selecting) return;

    event.preventDefault();

    void show(drawer, row, new URL(row.dataset.docHref ?? '', document.baseURI).href);
  });

  // The dialog script closes and opens a drawer that the server rendered
  // open, to make it a modal. Only a drawer that stays closed loses its mark.
  document.addEventListener(DOC_SAVED, () => void refresh());

  drawer.addEventListener('close', () => {
    if (!drawer.open) mark(null);
  });

  // The filter of the list hides the entries that do not have the text.
  drawer.addEventListener('input', event => {
    const box = event.target;

    if (!(box instanceof HTMLInputElement) || box.dataset.docListFilter === undefined) return;

    const needle = box.value.trim().toLowerCase();

    for (const item of drawer.querySelectorAll<HTMLElement>(`${LIST} li`)) {
      item.hidden = needle !== '' && !(item.textContent ?? '').toLowerCase().includes(needle);
    }
  });

  // Arrow up and down in the list open the document before or after.
  drawer.addEventListener('keydown', event => {
    const from = event.target instanceof Element ? event.target.closest<HTMLElement>(`${LIST} a${ROW}`) : null;

    if (!from || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return;

    const entries = [...drawer.querySelectorAll<HTMLElement>(`${LIST} li:not([hidden]) a${ROW}`)];
    const next = entries[entries.indexOf(from) + (event.key === 'ArrowDown' ? 1 : -1)];

    if (!next) return;

    event.preventDefault();
    next.focus();

    void show(drawer, next, new URL(next.dataset.docHref ?? '', document.baseURI).href);
  });
}
