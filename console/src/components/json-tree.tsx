import type { FC } from 'hono/jsx';
import { useEffect, useRef, useState } from 'hono/jsx';

import { addField, containerIds, deepEqual, flatten, getIn, parseScalar, pathId, removeIn, type Path, type Row, scalarText, setIn } from './json-model';
import { filterRows } from './tree-filter';
import { noSpelling } from '@/ui/input';

const LOCKED = new Set(['_id', '_v']);

function lockedRow(row: Row): boolean {
  return row.depth === 0 && LOCKED.has(row.key);
}

export interface TreeRowsProps {
  rows: Row[];
  emphasizeTop?: boolean;
  changed?: ReadonlySet<string>;
  editable?: boolean;
  editing?: string | null;
  draft?: string;
  onToggle?: (row: Row) => void;
  onEdit?: (row: Row) => void;
  onDraft?: (text: string) => void;
  onCommit?: (row: Row) => void;
  onCancel?: () => void;
  onRemove?: (row: Row) => void;
  onAdd?: (row: Row) => void;
}

const Caret: FC<{ row: Row; onToggle?: (row: Row) => void }> = ({ row, onToggle }) => {
  if (!row.expandable) return <span class="tcaret" />;

  if (!onToggle) {
    return (
      <span class="tcaret" aria-hidden="true">
        {row.open ? '▾' : '▸'}
      </span>
    );
  }

  return (
    <button type="button" class="tcaret" aria-label={row.open ? 'Collapse' : 'Expand'} aria-expanded={row.open} onClick={() => onToggle(row)}>
      {row.open ? '▾' : '▸'}
    </button>
  );
};

const ValueInput: FC<{ draft: string; onDraft: (text: string) => void; onCommit: () => void; onCancel: () => void }> = ({ draft, onDraft, onCommit, onCancel }) => {
  const input = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);

  const keys = (event: KeyboardEvent) => {
    if (event.key === 'Enter') onCommit();

    if (event.key === 'Escape') onCancel();
  };

  return <input ref={input} class="tinput" data-1p-ignore data-lpignore="true" data-bwignore data-form-type="other" {...noSpelling} value={draft} aria-label="Value" onInput={(event: Event) => onDraft((event.target as HTMLInputElement).value)} onKeyDown={keys} onBlur={onCommit} />;
};

// TreeRows draws the rows. It has no state, so the server uses it for the
// read-only fallback and the island uses it with handlers.
export const TreeRows: FC<TreeRowsProps> = props => {
  const { rows, emphasizeTop = false, changed, editable = false, editing = null, draft = '' } = props;

  return (
    <div class="tree" role="tree">
      {rows.map(row => {
        const top = emphasizeTop && row.depth === 0;
        const marked = changed?.has(row.id) ?? false;
        const canEdit = editable && !row.expandable && !lockedRow(row) && Boolean(props.onEdit);
        const canRemove = editable && !lockedRow(row) && row.path.length > 0 && Boolean(props.onRemove);
        const canAdd = editable && row.expandable && Boolean(props.onAdd);

        const rowStyle = `padding-left:${14 + row.depth * 20}px`;
        const rowClass = ['trow', top ? 'top' : '', marked ? 'changed' : ''].filter(Boolean).join(' ');
        const keyClass = ['tkey', row.key.startsWith('_') ? 'dim' : '', top ? 'emph' : ''].filter(Boolean).join(' ');
        const valueClass = ['tval', `k-${row.kind}`, canEdit ? 'editable' : ''].filter(Boolean).join(' ');

        return (
          <div class={rowClass} style={rowStyle} role="treeitem" aria-expanded={row.expandable ? row.open : undefined}>
            <Caret row={row} onToggle={props.onToggle} />

            {row.key !== '' && <span class={keyClass}>{row.key}</span>}
            {row.key !== '' && !top && <span class="tsep">:</span>}

            {editing === row.id ? (
              <ValueInput draft={draft} onDraft={props.onDraft ?? (() => undefined)} onCommit={() => props.onCommit?.(row)} onCancel={props.onCancel ?? (() => undefined)} />
            ) : (
              <span class={valueClass} title={canEdit ? 'Click to edit' : undefined} onClick={canEdit ? () => props.onEdit?.(row) : undefined}>
                {row.summary}
              </span>
            )}

            <span class="ttype">{row.kind}</span>

            {(canAdd || canRemove) && (
              <span class="tactions">
                {canAdd && (
                  <button type="button" class="ticon" title="Add a field" aria-label={`Add a field to ${row.key || 'the document'}`} onClick={() => props.onAdd?.(row)}>
                    +
                  </button>
                )}

                {canRemove && (
                  <button type="button" class="ticon" title="Remove" aria-label={`Remove ${row.key}`} onClick={() => props.onRemove?.(row)}>
                    ×
                  </button>
                )}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
};

// StaticTree is the read-only tree with every container open. The server
// renders it before any script runs.
export const StaticTree: FC<{ value: unknown; emphasizeTop?: boolean }> = ({ value, emphasizeTop }) => <TreeRows rows={flatten(value, new Set())} emphasizeTop={emphasizeTop} />;

export interface JsonTreeProps {
  value: unknown;
  editable?: boolean;
  onChange?: (next: unknown) => void;
  baseline?: unknown;
  toolbar?: boolean;
  filterable?: boolean;
  emphasizeTop?: boolean;
}

interface AddState {
  path: Path;
  isArray: boolean;
}

const AddRow: FC<{ state: AddState; onSubmit: (key: string, value: string) => void; onCancel: () => void }> = ({ state, onSubmit, onCancel }) => {
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');

  const keys = (event: KeyboardEvent) => {
    if (event.key === 'Enter') onSubmit(key.trim(), value);

    if (event.key === 'Escape') onCancel();
  };

  return (
    <div class="tadd">
      {!state.isArray && <input class="tinput" data-1p-ignore data-lpignore="true" data-bwignore data-form-type="other" {...noSpelling} placeholder="field" aria-label="Field name" value={key} onInput={(event: Event) => setKey((event.target as HTMLInputElement).value)} onKeyDown={keys} />}

      <input class="tinput" data-1p-ignore data-lpignore="true" data-bwignore data-form-type="other" {...noSpelling} placeholder="value (JSON or text)" aria-label="Field value" value={value} onInput={(event: Event) => setValue((event.target as HTMLInputElement).value)} onKeyDown={keys} />

      <button type="button" class="tbtn" onClick={() => onSubmit(key.trim(), value)}>
        Add
      </button>
      <button type="button" class="tbtn" onClick={onCancel}>
        Cancel
      </button>
    </div>
  );
};

// JsonTree shows a value as rows with a caret, key, colored value, and type.
// Expand all and Collapse are in the toolbar. With filterable, the toolbar
// starts with a box that narrows the rows to the keys or values that have
// its text. With editable and onChange, a click on a scalar edits it, and
// rows can be added or removed. baseline marks the rows that differ from it.
export const JsonTree: FC<JsonTreeProps> = ({ value, editable = false, onChange, baseline, toolbar = true, filterable = false, emphasizeTop = false }) => {
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [adding, setAdding] = useState<AddState | null>(null);
  const [query, setQuery] = useState('');
  const active = useRef<string | null>(null);

  const rows = filterRows(value, closed, filterable ? query : '');
  const noMatch = rows.length === 0 && query.trim() !== '';
  const changed = baseline === undefined ? undefined : changedIds(rows, value, baseline);

  const toggle = (row: Row) => {
    const next = new Set(closed);

    if (next.has(row.id)) next.delete(row.id);
    else next.add(row.id);

    setClosed(next);
  };

  const edit = (row: Row) => {
    active.current = row.id;

    setDraft(scalarText(row.value));
    setEditing(row.id);
  };

  const cancel = () => {
    active.current = null;

    setEditing(null);
  };

  const commit = (row: Row) => {
    if (active.current !== row.id) return;

    active.current = null;

    setEditing(null);

    const next = parseScalar(draft);

    if (!deepEqual(next, row.value)) onChange?.(setIn(value, row.path, next));
  };

  const remove = (row: Row) => onChange?.(removeIn(value, row.path));

  const add = (row: Row) => setAdding({ path: row.path, isArray: Array.isArray(row.value) });

  const submitAdd = (key: string, text: string) => {
    if (!adding || (!adding.isArray && key === '')) return;

    setAdding(null);
    onChange?.(addField(value, adding.path, key, parseScalar(text)));
  };

  const rootAdd = () => setAdding({ path: [], isArray: Array.isArray(value) });

  return (
    <div class="jt">
      {toolbar && (
        <div class="jt-bar">
          {filterable && (
            <input
              class="input jt-filter"
              type="search"
              {...noSpelling}
              placeholder="Filter fields"
              aria-label="Filter fields"
              value={query}
              onInput={(event: Event) => setQuery((event.target as HTMLInputElement).value)}
            />
          )}

          <button type="button" class="tbtn" onClick={() => setClosed(new Set())}>
            Expand all
          </button>
          <button type="button" class="tbtn" onClick={() => setClosed(new Set(containerIds(value)))}>
            Collapse
          </button>

          {editable && (
            <button type="button" class="tbtn" onClick={rootAdd}>
              + Field
            </button>
          )}
        </div>
      )}

      <TreeRows
        rows={rows}
        emphasizeTop={emphasizeTop}
        changed={changed}
        editable={editable}
        editing={editing}
        draft={draft}
        onToggle={toggle}
        onEdit={edit}
        onDraft={setDraft}
        onCommit={commit}
        onCancel={cancel}
        onRemove={remove}
        onAdd={add}
      />

      {noMatch && <p class="hint jt-none">No field or value has this text.</p>}

      {adding && <AddRow state={adding} onSubmit={submitAdd} onCancel={() => setAdding(null)} />}
    </div>
  );
};

// changedIds marks every row that differs from the baseline, and its parents.
function changedIds(rows: Row[], value: unknown, baseline: unknown): Set<string> {
  const ids = new Set<string>();

  for (const row of rows) {
    if (!deepEqual(getIn(baseline, row.path), row.value)) ids.add(row.id);
  }

  for (const id of ids) {
    const parts = id.split('\u0001');

    for (let length = 1; length < parts.length; length++) ids.add(pathId(parts.slice(0, length)));
  }

  return ids;
}
