import type { Child, FC } from 'hono/jsx';

import { Icon } from './icon';

export interface DataColumn {
  id: string;
  label: string;
  href?: string;
  sort?: 'asc' | 'desc';
  align?: 'right';
}

export interface DataTableProps {
  id: string;
  label: string;
  columns: DataColumn[];
  minWidth?: number;
  title?: Child;
  actions?: Child;
  footer?: Child;
  empty?: Child;
  selectable?: boolean;
  bulk?: Child;
  children?: Child;
}

interface TableState {
  hidden: string[];
  widths: Record<string, number>;
}

const MIN_WIDTH = 64;
const CHECK_WIDTH = 40;

// DataTable is a table for many columns. The reader can hide columns in the
// Columns menu and drag the edge of a header to change its width. The
// browser keeps both choices for each id. A header with href is a sort
// link. Each cell of a row must have data-col with the id of its column.
// With empty, that content shows in place of the table and its menu.
// With selectable, the table has a first column of checkboxes: the header
// has "select all", and each row starts with a RowCheck. bulk is the bar of
// actions on the selected rows; it shows only while a row is selected. The
// text of an element with data-bulk-count is the number of selected rows.
export const DataTable: FC<DataTableProps> = ({ id, label, columns, minWidth = 0, title, actions, footer, empty, selectable = false, bulk, children }) => {
  const style = minWidth > 0 ? `min-width:${minWidth}px` : undefined;

  return (
    <div class="dtable" data-table={id}>
      <div class="dtable-bar">
        <div class="dtable-title">{title}</div>

        {!empty && <ColumnMenu columns={columns} />}

        {actions}
      </div>

      {bulk && !empty && <div class="dtable-bulk">{bulk}</div>}

      {empty && <div class="dtable-empty">{empty}</div>}

      <div class="scroll" hidden={Boolean(empty)}>
        <table style={style} aria-label={label}>
          <thead>
            <tr>
              {selectable && (
                <th class="dtable-check">
                  <input type="checkbox" data-select-all aria-label="Select all rows" />
                </th>
              )}

              {columns.map(column => (
                <HeaderCell column={column} />
              ))}
            </tr>
          </thead>

          <tbody>{children}</tbody>
        </table>
      </div>

      {footer}
    </div>
  );
};

// RowCheck is the first cell of a row in a selectable table. form is the id
// of the form of the bulk bar, which gets the checked values under name.
export const RowCheck: FC<{ name: string; value: string; form: string; label: string }> = ({ name, value, form, label }) => (
  <td class="dtable-check">
    <input type="checkbox" name={name} value={value} form={form} aria-label={label} />
  </td>
);

const HeaderCell: FC<{ column: DataColumn }> = ({ column }) => {
  const arrow = column.sort === 'asc' ? <Icon name="arrow-up" size={14} /> : column.sort === 'desc' ? <Icon name="arrow-down" size={14} /> : null;
  const sorted = column.sort === 'asc' ? 'ascending' : column.sort === 'desc' ? 'descending' : undefined;
  const className = column.align === 'right' ? 'mono num' : 'mono';

  return (
    <th class={className} data-col={column.id} aria-sort={sorted}>
      {column.href ? (
        <a class="dtable-sort" href={column.href} title={`Sort by ${column.label}`}>
          {column.label}
          {arrow}
        </a>
      ) : (
        column.label
      )}

      <span class="dtable-grip" data-grip aria-hidden="true" />
    </th>
  );
};

const ColumnMenu: FC<{ columns: DataColumn[] }> = ({ columns }) => (
  <details class="dtable-menu">
    <summary class="btn small">
      <Icon name="columns" />
      Columns
    </summary>

    <div class="dtable-menu-panel">
      {columns.map(column => (
        <label class="check mono">
          <input type="checkbox" data-col-toggle={column.id} checked />
          {column.label}
        </label>
      ))}

      <button type="button" class="btn small ghost" data-col-reset>
        Reset columns
      </button>
    </div>
  </details>
);

function read(key: string): TableState {
  try {
    const value = JSON.parse(localStorage.getItem(key) ?? '{}');

    return { hidden: Array.isArray(value.hidden) ? value.hidden : [], widths: value.widths && typeof value.widths === 'object' ? value.widths : {} };
  } catch {
    return { hidden: [], widths: {} };
  }
}

function write(key: string, state: TableState): void {
  try {
    localStorage.setItem(key, JSON.stringify(state));
  } catch {
    // Storage can be blocked. The table still works for this page view.
  }
}

function headers(host: HTMLElement): HTMLElement[] {
  return [...host.querySelectorAll<HTMLElement>('th[data-col]')];
}

// apply shows the state: a style rule hides the cells of each hidden column,
// and a fixed layout holds the widths that the reader set.
function apply(host: HTMLElement, sheet: HTMLStyleElement, state: TableState): void {
  const id = host.dataset.table ?? '';

  sheet.textContent = state.hidden.map(column => `[data-table=${JSON.stringify(id)}] [data-col=${JSON.stringify(column)}]{display:none}`).join('');

  for (const box of host.querySelectorAll<HTMLInputElement>('[data-col-toggle]')) {
    box.checked = !state.hidden.includes(box.dataset.colToggle ?? '');
  }

  const table = host.querySelector('table');
  const sized = Object.keys(state.widths).length > 0;

  if (!table) return;

  table.classList.toggle('dtable-fixed', sized);

  let total = host.querySelector('th.dtable-check') ? CHECK_WIDTH : 0;

  for (const cell of headers(host)) {
    const width = state.widths[cell.dataset.col ?? ''];

    cell.style.width = sized && width ? `${width}px` : '';

    if (!state.hidden.includes(cell.dataset.col ?? '')) total += width ?? MIN_WIDTH;
  }

  table.style.width = sized ? `${total}px` : '';
}

// freeze stores the width that every header has now, so a drag of one
// column does not move the others.
function freeze(host: HTMLElement, state: TableState): void {
  for (const cell of headers(host)) {
    const column = cell.dataset.col ?? '';

    if (!state.widths[column] && cell.offsetWidth > 0) state.widths[column] = cell.offsetWidth;
  }
}

function rowChecks(host: HTMLElement): HTMLInputElement[] {
  return [...host.querySelectorAll<HTMLInputElement>('td.dtable-check input')];
}

// showSelection puts the number of selected rows in the bulk bar and sets
// the "select all" box: checked for all rows, a dash for some of them.
function showSelection(host: HTMLElement): void {
  const boxes = rowChecks(host);
  const count = boxes.filter(box => box.checked).length;
  const all = host.querySelector<HTMLInputElement>('[data-select-all]');

  if (all) {
    all.checked = count > 0 && count === boxes.length;
    all.indeterminate = count > 0 && count < boxes.length;
  }

  for (const label of host.querySelectorAll<HTMLElement>('[data-bulk-count]')) label.textContent = String(count);
}

function upgrade(host: HTMLElement): void {
  const key = `ndb-ui:table:${host.dataset.table}`;
  const sheet = document.createElement('style');

  let state = read(key);

  document.head.append(sheet);
  apply(host, sheet, state);

  if (host.querySelector('th.dtable-check')) showSelection(host);

  host.addEventListener('change', event => {
    const box = event.target;

    if (!(box instanceof HTMLInputElement)) return;

    if (box.dataset.selectAll !== undefined) {
      for (const row of rowChecks(host)) row.checked = box.checked;
    }

    if (box.closest('.dtable-check')) {
      showSelection(host);

      return;
    }

    if (box.dataset.colToggle === undefined) return;

    const column = box.dataset.colToggle;

    state.hidden = box.checked ? state.hidden.filter(item => item !== column) : [...state.hidden, column];

    write(key, state);
    apply(host, sheet, state);
  });

  host.addEventListener('click', event => {
    const target = event.target instanceof Element ? event.target : null;

    if (!target?.closest('[data-col-reset]')) return;

    state = { hidden: [], widths: {} };

    write(key, state);
    apply(host, sheet, state);
  });

  host.addEventListener('pointerdown', event => {
    const grip = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-grip]') : null;
    const cell = grip?.closest<HTMLElement>('th[data-col]');

    if (!grip || !cell) return;

    event.preventDefault();
    freeze(host, state);

    const column = cell.dataset.col ?? '';
    const startX = event.clientX;
    const startWidth = state.widths[column] ?? cell.offsetWidth;

    const move = (moved: PointerEvent) => {
      state.widths[column] = Math.max(MIN_WIDTH, Math.round(startWidth + moved.clientX - startX));

      apply(host, sheet, state);
    };

    const stop = () => {
      document.removeEventListener('pointermove', move);
      document.removeEventListener('pointerup', stop);

      write(key, state);
    };

    document.addEventListener('pointermove', move);
    document.addEventListener('pointerup', stop);
  });
}

export function mount(): void {
  for (const host of document.querySelectorAll<HTMLElement>('[data-table]')) upgrade(host);

  document.addEventListener('click', event => {
    const inside = event.target instanceof Element ? event.target.closest('.dtable-menu') : null;

    for (const menu of document.querySelectorAll<HTMLDetailsElement>('.dtable-menu[open]')) {
      if (menu !== inside) menu.open = false;
    }
  });
}
