import type { FC } from 'hono/jsx';

import { type ComboOption, customValue, filterOptions, pick, splitList, step } from '@/components/combobox-model';

import { Input } from './input';

export type { ComboOption };

export interface ComboboxProps {
  id: string;
  options: ComboOption[];
  value?: string;
  // multiple lets the value be a comma list: the list then filters on the
  // last item, and a pick replaces only that item.
  multiple?: boolean;
  mono?: boolean;
  [attribute: string]: unknown;
}

// Combobox is a text field with a list of options under it. The user can
// pick an option or type any other value. The server renders an input with a
// native datalist, so it works without the script. The script puts a list
// with the look of Select in its place.
export const Combobox: FC<ComboboxProps> = ({ id, options, value, multiple = false, mono = false, ...rest }) => {
  const listId = `${id}-list`;

  return (
    <div
      class="dd cb"
      data-cb
      data-cb-multiple={multiple ? true : undefined}
      data-cb-options={JSON.stringify(options)}
    >
      <Input
        id={id}
        mono={mono}
        list={listId}
        value={value}
        autocomplete="off"
        {...rest}
      />

      <datalist id={listId}>
        {options.map(option => (
          <option value={option.id} label={option.hint} />
        ))}
      </datalist>
    </div>
  );
};

interface Row {
  value: string;
  option?: ComboOption;
}

interface Combo {
  host: HTMLElement;
  input: HTMLInputElement;
  list: HTMLUListElement;
  options: ComboOption[];
  multiple: boolean;
  rows: Row[];
  active: number;
}

function readOptions(host: HTMLElement): ComboOption[] {
  try {
    const value = JSON.parse(host.dataset.cbOptions ?? '[]');

    return Array.isArray(value) ? (value as ComboOption[]) : [];
  } catch {
    return [];
  }
}

function close(combo: Combo): void {
  combo.host.classList.remove('open');
  combo.input.setAttribute('aria-expanded', 'false');
  combo.input.removeAttribute('aria-activedescendant');
  combo.active = -1;
}

function isOpen(combo: Combo): boolean {
  return combo.host.classList.contains('open');
}

function optionRow(combo: Combo, row: Row, index: number): HTMLLIElement {
  const item = document.createElement('li');
  const name = document.createElement('span');

  name.className = row.option ? 'cb-name mono' : 'cb-name';
  name.textContent = row.option ? row.value : `Use "${row.value}"`;

  item.id = `${combo.list.id}-${index}`;
  item.className = ['dd-option', 'cb-option', row.option ? '' : 'cb-custom', index === combo.active ? 'on' : ''].filter(Boolean).join(' ');
  item.setAttribute('role', 'option');
  item.setAttribute('aria-selected', String(index === combo.active));
  item.dataset.index = String(index);
  item.append(name);

  if (row.option?.badge) {
    const badge = document.createElement('span');

    badge.className = 'badge pink';
    badge.textContent = row.option.badge;

    item.append(badge);
  }

  if (row.option?.hint) {
    const hint = document.createElement('span');

    hint.className = 'cb-hint';
    hint.textContent = row.option.hint;

    item.append(hint);
  }

  return item;
}

// paint builds the rows for the text of the input: the options that match,
// then the row for the typed value.
function paint(combo: Combo): void {
  const text = combo.input.value;
  const custom = customValue(combo.options, text);

  const found = filterOptions(combo.options, splitList(text, combo.multiple).part).map(option => ({ value: option.id, option }));

  combo.rows = custom === undefined ? found : [...found, { value: custom }];
  combo.active = Math.min(combo.active, combo.rows.length - 1);

  combo.list.replaceChildren(...combo.rows.map((row, index) => optionRow(combo, row, index)));

  const active = combo.list.querySelector<HTMLElement>('.on');

  if (active) {
    combo.input.setAttribute('aria-activedescendant', active.id);
    active.scrollIntoView({ block: 'nearest' });
  } else {
    combo.input.removeAttribute('aria-activedescendant');
  }
}

function open(combo: Combo): void {
  paint(combo);

  if (combo.rows.length === 0) {
    close(combo);

    return;
  }

  combo.host.classList.add('open');
  combo.input.setAttribute('aria-expanded', 'true');
}

// choose writes the value of a row and tells the form, so a script that
// listens for input sees the new value.
function choose(combo: Combo, index: number): void {
  const row = combo.rows[index];

  if (!row) return;

  combo.input.value = row.option ? pick(combo.input.value, row.value, combo.multiple) : row.value;

  combo.input.dispatchEvent(new Event('input', { bubbles: true }));
  combo.input.dispatchEvent(new Event('change', { bubbles: true }));

  close(combo);
}

function onKey(combo: Combo, event: KeyboardEvent): void {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();

    if (!isOpen(combo)) open(combo);

    combo.active = step(combo.active, combo.rows.length, event.key === 'ArrowDown' ? 1 : -1);
    paint(combo);

    return;
  }

  if (!isOpen(combo)) return;

  if (event.key === 'Escape') {
    event.preventDefault();
    close(combo);

    return;
  }

  if (event.key === 'Tab') close(combo);

  if (event.key === 'Enter') {
    if (combo.active >= 0) {
      event.preventDefault();
      choose(combo, combo.active);

      return;
    }

    close(combo);
  }
}

const combos = new Set<Combo>();

function upgrade(host: HTMLElement): void {
  const input = host.querySelector('input');

  if (!input || host.querySelector('.cb-list')) return;

  const list = document.createElement('ul');

  list.id = `${input.id || 'cb'}-options`;
  list.className = 'dd-list cb-list';
  list.setAttribute('role', 'listbox');
  list.setAttribute('aria-label', input.getAttribute('aria-label') ?? 'Options');

  // The native datalist would open a second list over this one.
  input.removeAttribute('list');
  input.setAttribute('role', 'combobox');
  input.setAttribute('aria-autocomplete', 'list');
  input.setAttribute('aria-expanded', 'false');
  input.setAttribute('aria-controls', list.id);

  host.append(list);

  const combo: Combo = { host, input, list, options: readOptions(host), multiple: host.dataset.cbMultiple !== undefined, rows: [], active: -1 };

  combos.add(combo);

  input.addEventListener('input', () => {
    combo.active = -1;
    open(combo);
  });

  input.addEventListener('click', () => open(combo));
  input.addEventListener('keydown', event => onKey(combo, event));

  host.addEventListener('focusout', event => {
    if (!(event.relatedTarget instanceof Node) || !host.contains(event.relatedTarget)) close(combo);
  });

  // The mouse must not take the focus from the input.
  list.addEventListener('mousedown', event => event.preventDefault());

  list.addEventListener('click', event => {
    const item = event.target instanceof Element ? event.target.closest<HTMLElement>('.cb-option') : null;

    event.preventDefault();

    if (!item) return;

    choose(combo, Number(item.dataset.index));
    input.focus();
  });
}

export function mount(): void {
  for (const host of document.querySelectorAll<HTMLElement>('[data-cb]')) upgrade(host);

  document.addEventListener('click', event => {
    const target = event.target instanceof Node ? event.target : null;

    for (const combo of combos) {
      if (!target || !combo.host.contains(target)) close(combo);
    }
  });
}
