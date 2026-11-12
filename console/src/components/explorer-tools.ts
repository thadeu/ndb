// The browser part of the explorer page: the favorite stars of the collection
// list, the field chips and the request preview of the New index form, and
// the local time of a restore instant.
// Without it the list shows every collection and the form is a text field.
import { favoritesKey, fieldsError, indexRequest, parseFields, requestText, toggleFavorite } from '@/collections';

function readFavorites(key: string): string[] {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(key) ?? '[]');

    return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
  } catch {
    return [];
  }
}

function saveFavorites(key: string, list: string[]): void {
  try {
    window.localStorage.setItem(key, JSON.stringify(list));
  } catch {
    return;
  }
}

function mountFavorites(host: HTMLElement): void {
  const key = favoritesKey(host.dataset.favorites ?? '');
  const onlyFavorites = host.dataset.show === 'favorites';
  const empty = host.querySelector<HTMLElement>('[data-favorites-empty]');

  const paint = () => {
    const list = readFavorites(key);
    let shown = 0;

    for (const row of host.querySelectorAll<HTMLElement>('[data-collection]')) {
      const on = list.includes(row.dataset.collection ?? '');
      const star = row.querySelector<HTMLElement>('[data-favorite]');

      row.hidden = onlyFavorites && !on;
      star?.classList.toggle('on', on);
      star?.setAttribute('aria-pressed', String(on));

      if (!row.hidden) shown++;
    }

    if (empty) empty.hidden = !onlyFavorites || shown > 0;
  };

  host.addEventListener('click', event => {
    const star = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-favorite]') : null;

    if (!star) return;

    event.preventDefault();
    saveFavorites(key, toggleFavorite(readFavorites(key), star.dataset.favorite ?? ''));
    paint();
  });

  for (const star of host.querySelectorAll<HTMLElement>('[data-favorite]')) star.hidden = false;

  paint();
}

function chip(position: number, field: string): HTMLElement {
  const item = document.createElement('span');
  const order = document.createElement('span');

  item.className = 'field-chip mono';
  order.className = 'field-chip-order';
  order.textContent = String(position);

  item.append(order, ` ${field}`);

  return item;
}

function mountIndexForm(form: HTMLFormElement): void {
  const collection = form.dataset.indexForm ?? '';
  const input = form.querySelector<HTMLInputElement>('[data-index-input]');
  const chips = form.querySelector<HTMLElement>('[data-index-chips]');
  const unique = form.querySelector<HTMLInputElement>('[name="unique"]');
  const preview = form.querySelector<HTMLElement>('[data-index-preview]');
  const hint = form.querySelector<HTMLElement>('[data-index-error]');

  if (!input || !chips || !preview) return;

  const value = document.createElement('input');

  value.type = 'hidden';
  value.name = 'fields';
  input.removeAttribute('name');
  input.required = false;
  form.append(value);

  let committed = parseFields(input.value);

  input.value = '';

  const paint = () => {
    const fields = parseFields([...committed, input.value].join(','));
    const error = fields.length > 0 ? fieldsError(fields) : undefined;

    value.value = fields.join(',');
    chips.replaceChildren(...committed.map((field, index) => chip(index + 1, field)));
    preview.textContent = requestText(indexRequest(collection, fields.length > 0 ? fields : ['{field}'], unique?.checked ?? false));

    if (hint) {
      hint.textContent = error ?? '';
      hint.hidden = !error;
    }
  };

  const commit = () => {
    committed = parseFields([...committed, input.value].join(','));
    input.value = '';
  };

  input.addEventListener('input', () => {
    if (/[\s,]/.test(input.value)) commit();

    paint();
  });

  input.addEventListener('keydown', event => {
    if (event.key === 'Backspace' && input.value === '' && committed.length > 0) {
      committed = committed.slice(0, -1);
      paint();

      return;
    }

    if (event.key === 'Enter' && input.value.trim() !== '') {
      event.preventDefault();
      commit();
      paint();
    }
  });

  input.addEventListener('blur', () => {
    commit();
    paint();
  });

  unique?.addEventListener('change', paint);

  paint();
}

// mountLocalTimes writes the time of the browser next to a UTC instant, as
// the confirm step of a restore shows it. Without it the page shows UTC only.
function mountLocalTimes(): void {
  for (const time of document.querySelectorAll<HTMLTimeElement>('time[data-local-time]')) {
    const date = new Date(time.dateTime);

    if (Number.isNaN(date.getTime())) continue;

    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const text = date.toLocaleString('en-US', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });

    time.textContent = `${text} your time (${zone})`;
  }
}

export function mount(): void {
  mountLocalTimes();

  for (const host of document.querySelectorAll<HTMLElement>('[data-favorites]')) mountFavorites(host);

  for (const form of document.querySelectorAll<HTMLFormElement>('form[data-index-form]')) mountIndexForm(form);
}
