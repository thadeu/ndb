import type { Child, FC } from 'hono/jsx';

import { Icon } from './icon';

export interface DataColumn {
  id: string;
  label: string;
  href?: string;
  sort?: 'asc' | 'desc';
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
  children?: Child;
}

interface TableState {
  hidden: string[];
  widths: Record<string, number>;
}

const MIN_WIDTH = 64;

// DataTable is a table for many columns. The reader can hide columns in the
// Columns menu and drag the edge of a header to change its width. The
// browser keeps both choices for each id. A header with href is a sort
// link. Each cell of a row must have data-col with the id of its column.
// With empty, that content shows in place of the table and its menu.
export const DataTable: FC<DataTableProps> = ({ id, label, columns, minWidth = 0, title, actions, footer, empty, children }) => {
  const style = minWidth > 0 ? `min-width:${minWidth}px` : undefined;

  return (
    <div class="dtable" data-table={id}>
      <div class="dtable-bar">
        <div class="dtable-title">{title}</div>

        {!empty && <ColumnMenu columns={columns} />}

        {actions}
      </div>

      {empty && <div class="dtable-empty">{empty}</div>}

      <div class="scroll" hidden={Boolean(empty)}>
        <table style={style} aria-label={label}>
          <thead>
            <tr>
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

const HeaderCell: FC<{ column: DataColumn }> = ({ column }) => {
  const arrow = column.sort === 'asc' ? <Icon name="arrow-up" size={14} /> : column.sort === 'desc' ? <Icon name="arrow-down" size={14} /> : null;
  const sorted = column.sort === 'asc' ? 'ascending' : column.sort === 'desc' ? 'descending' : undefined;

  return (
    <th class="mono" data-col={column.id} aria-sort={sorted}>
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

  let total = 0;

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

function upgrade(host: HTMLElement): void {
  const key = `ndb-ui:table:${host.dataset.table}`;
  const sheet = document.createElement('style');

  let state = read(key);

  document.head.append(sheet);
  apply(host, sheet, state);

  host.addEventListener('change', event => {
    const box = event.target;

    if (!(box instanceof HTMLInputElement) || box.dataset.colToggle === undefined) return;

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
