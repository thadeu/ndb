// The browser part of the explorer page: the favorite stars of the collection
// list, the field chips and the request preview of the New index form, and
// the local time of a restore instant.
// Without it the list shows every collection and the form is a text field.
import {
  type CollectionDefinition,
  definitionBody,
  encryptionDraft,
  encryptionError,
  favoritesKey,
  fieldsError,
  indexFieldsError,
  indexRequest,
  lines,
  parseFields,
  requestText,
  toggleFavorite,
} from '@/collections';
import { upgrade as upgradeSelect } from '@/ui/select';

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
  const locks = readJson<Pick<CollectionDefinition, 'encrypted' | 'blind'>>(form.dataset.indexLocks);

  if (!input || !chips || !preview) return;

  const value = document.createElement('input');

  value.type = 'hidden';
  value.name = 'fields';
  input.removeAttribute('name');
  input.required = false;
  form.append(value);

  let committed = parseFields(input.value);

  input.value = '';

  const errorOf = (fields: string[]) => (fields.length > 0 ? (fieldsError(fields) ?? indexFieldsError(fields, locks)) : undefined);

  const paint = () => {
    const fields = parseFields([...committed, input.value].join(','));
    const error = errorOf(fields);

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

  form.addEventListener('click', event => {
    const pick = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-index-pick]') : null;

    if (!pick) return;

    committed = parseFields([...committed, pick.dataset.indexPick ?? ''].join(','));
    paint();
    input.focus();
  });

  form.addEventListener('submit', event => {
    commit();
    paint();

    if (errorOf(committed)) event.preventDefault();
  });

  paint();
}

function readJson<T>(text: string | undefined): T | undefined {
  try {
    return text ? (JSON.parse(text) as T) : undefined;
  } catch {
    return undefined;
  }
}

function formValues(form: HTMLFormElement): Record<string, string> {
  const values: Record<string, string> = {};

  for (const [key, value] of new FormData(form)) {
    if (typeof value === 'string') values[key] = value;
  }

  return values;
}

// mountEncryptionFields adds blind rows on a click, checks the encrypted and
// blind fields as the user types, and shows the real PUT body of a new
// collection. The server checks the same rules again.
function mountEncryptionFields(fieldset: HTMLElement): void {
  const form = fieldset.closest('form');
  const rows = fieldset.querySelector<HTMLElement>('[data-blind-rows]');
  const add = fieldset.querySelector<HTMLButtonElement>('[data-blind-add]');
  const hint = fieldset.querySelector<HTMLElement>('[data-encryption-error]');
  const preview = form?.querySelector<HTMLElement>('[data-create-preview]');
  const fixed = readJson<Partial<CollectionDefinition>>(fieldset.closest<HTMLElement>('[data-encryption-target]')?.dataset.encryptionTarget);
  const known = (fieldset.dataset.encryptionFields ?? '').split(',').filter(Boolean);

  if (!form || !rows || !add) return;

  add.hidden = false;

  add.addEventListener('click', () => {
    const first = rows.querySelector<HTMLElement>('[data-blind-row]');

    if (!first) return;

    const row = first.cloneNode(true) as HTMLElement;
    const n = rows.querySelectorAll('[data-blind-row]').length;

    for (const control of row.querySelectorAll<HTMLInputElement | HTMLSelectElement>('input, select')) {
      control.name = control.name.replace(/_\d+$/, `_${n}`);
      control.id = control.id.replace(/-\d+$/, `-${n}`);
      control.value = control instanceof HTMLSelectElement ? 'exact' : '';
    }

    for (const part of row.querySelectorAll('.dd-button, .dd-list')) part.remove();

    rows.append(row);

    for (const host of row.querySelectorAll<HTMLElement>('[data-dd]')) upgradeSelect(host);

    row.querySelector<HTMLInputElement>('input')?.focus();
    paint();
  });

  const paint = () => {
    const values = formValues(form);
    const draft = encryptionDraft(values);
    const target = fixed ?? { time: values.time, expires_field: values.expires_field, write: values.write, indexes: lines(values.indexes ?? ''), unique: lines(values.unique ?? '') };
    const again = [...draft.encrypted, ...Object.keys(draft.blind)].find(field => known.includes(field) && !(field in draft.blind));
    const error = encryptionError(draft, target) ?? (again ? `${again} is encrypted already.` : undefined);

    if (hint) {
      hint.textContent = error ?? '';
      hint.hidden = !error;
    }

    if (preview) {
      const name = (values.collection ?? '').trim() || '{name}';

      preview.textContent = `PUT /${name}\n${JSON.stringify(definitionBody(values))}`;
    }
  };

  form.addEventListener('input', paint);
  form.addEventListener('change', paint);

  form.addEventListener('submit', event => {
    paint();

    if (hint && !hint.hidden) event.preventDefault();
  });

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

  for (const fieldset of document.querySelectorAll<HTMLElement>('[data-encryption-fields]')) mountEncryptionFields(fieldset);
}
