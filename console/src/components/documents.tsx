import type { FC } from 'hono/jsx';

import type { Current } from '@/html/layout';
import { StaticTree } from '@/components/json-tree';
import { byteSize } from '@/components/json-model';
import { cellText, columnsOf, type FilterRow, type FindQuery, findParams, DEFAULT_LIMIT, LIMITS, needsValue, OPERATORS, TYPES } from '@/components/filter-model';
import type { CollectionStats, Doc } from '@/ndb';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { CodeBlock } from '@/ui/code-block';
import { Dialog, openDialog } from '@/ui/dialog';
import { Drawer } from '@/ui/drawer';
import { DataTable } from '@/ui/data-table';
import { Empty } from '@/ui/empty';
import { Icon } from '@/ui/icon';
import { Input, noSpelling } from '@/ui/input';
import { Pagination } from '@/ui/pagination';
import { Select } from '@/ui/select';
import { Tabs } from '@/ui/tabs';
import { Textarea } from '@/ui/textarea';

export interface DocumentsProps {
  current: Current;
  collections: CollectionStats[];
  coll: string;
  find: FindQuery;
  body: Record<string, unknown>;
  whereError?: string;
  rows: Doc[];
  plan?: string;
  examined?: number;
  total?: number;
  next?: string;
  trail: string[];
  id?: string;
  selected?: Doc;
  selectedError?: string;
  notice?: string;
  insertOpen?: boolean;
  insertError?: string;
  findOpen?: boolean;
}

export interface DocsLink {
  trail?: string[];
  id?: string;
  insert?: boolean;
  done?: string;
}

// docsHref builds a link of the Documents tab of the Explorer that keeps the
// find query, the page and the open document in the URL. trail holds the
// cursors of the pages before this one and of this one, so Previous works.
export function docsHref(name: string, coll: string, find: FindQuery | undefined, link: DocsLink = {}): string {
  const params = new URLSearchParams({ coll, tab: 'documents' });

  if (find) {
    for (const [key, value] of findParams(find)) params.append(key, value);
  }

  for (const cursor of link.trail ?? []) params.append('after', cursor);

  if (link.id) params.set('id', link.id);

  if (link.insert) params.set('insert', '1');

  if (link.done) params.set('done', link.done);

  return `c/${encodeURIComponent(name)}/explorer?${params}`;
}

// browserHref is the short form for the routes that only know a collection,
// such as the redirect of the old Browser page and the write routes.
export function browserHref(name: string, query: { coll: string; where?: string; id?: string; after?: string; done?: string; insert?: boolean }): string {
  const find = query.where ? { mode: 'json' as const, rows: [], json: query.where, sort: '', dir: 'desc' as const, fields: '', limit: DEFAULT_LIMIT } : undefined;

  return docsHref(name, query.coll, find, { ...query, trail: query.after ? [query.after] : [] });
}

const FilterRowFields: FC<{ row: FilterRow; index: number }> = ({ row, index }) => {
  const valueHint = needsValue(row.op) ? 'Enter a value' : 'No value';

  return (
    <div class="find-row">
      <Input mono name="f" value={row.field} placeholder="Field, such as customer.tier" aria-label="Field name" />

      <Select name="op" value={row.op} options={OPERATORS} aria-label="Condition" />

      <Select name="t" value={row.type} options={TYPES} aria-label="Type" />

      <Input mono name="v" value={row.value} placeholder={valueHint} aria-label="Value" />

      <Button variant="ghost" size="sm" type="submit" name="remove" value={String(index)} aria-label="Remove this filter">
        Remove
      </Button>
    </div>
  );
};

const Builder: FC<{ rows: FilterRow[] }> = ({ rows }) => (
  <div class="find-builder">
    <p class="hint">All filters must match. For In list, put commas between the values.</p>

    {rows.map((row, index) => (
      <FilterRowFields row={row} index={index} />
    ))}

    <div>
      <Button size="sm" type="submit" name="add" value="1">
        <Icon name="plus" />
        Add filter
      </Button>
    </div>
  </div>
);

const JsonFilter: FC<{ json: string }> = ({ json }) => (
  <div class="find-builder">
    <p class="hint">A MongoDB filter, such as {'{"status":"paid","total":{"$gte":10}}'}.</p>

    <Textarea mono name="where" rows={5} value={json} placeholder='{"status":"pending"}' aria-label="Filter JSON" {...noSpelling} />
  </div>
);

const Options: FC<{ find: FindQuery }> = ({ find }) => {
  const directions = [
    { id: 'desc', label: 'Descending' },
    { id: 'asc', label: 'Ascending' },
  ];

  return (
    <div class="find-options">
      <label class="find-option">
        <span class="label">Sort by</span>
        <Input mono name="sort" value={find.sort} placeholder="Field, such as ts" />
      </label>

      <label class="find-option">
        <span class="label">Order</span>
        <Select name="dir" value={find.dir} options={directions} />
      </label>

      <label class="find-option">
        <span class="label">Fields</span>
        <Input mono name="fields" value={find.fields} placeholder="All fields" />
      </label>
    </div>
  );
};

// summaryOf is the one-line text of the find card while it is closed.
function summaryOf(find: FindQuery): string {
  const active = find.rows.filter(row => row.field.trim() !== '');
  const parts: string[] = [];

  if (find.mode === 'json' && find.json.trim() !== '') parts.push('JSON filter');

  if (find.mode === 'builder' && active.length > 0) parts.push(`${active.length} ${active.length === 1 ? 'filter' : 'filters'}`);

  if (find.sort) parts.push(`sort by ${find.sort}`);

  if (find.fields) parts.push('some fields');

  return parts.length > 0 ? parts.join(' · ') : 'No filter: all documents';
}

const FindForm: FC<DocumentsProps & { open: boolean; force: boolean }> = ({ current, coll, find, body, whereError, open, force }) => {
  const request = `POST /${coll}/_search`;
  const code = `${request}\n${JSON.stringify(body, null, 2)}`;

  const modes = [
    { id: 'builder', label: 'Filters', content: <Builder rows={find.rows} /> },
    { id: 'json', label: 'JSON', content: <JsonFilter json={find.json} /> },
  ];

  return (
    <form class="card find" method="get" action={`c/${encodeURIComponent(current.name)}/explorer`} data-find-form>
      <input type="hidden" name="coll" value={coll} />
      <input type="hidden" name="tab" value="documents" />
      <input type="hidden" name="limit" value={String(find.limit)} />

      <details class="find-fold" open={open} data-fold="find" data-fold-force={force ? true : undefined}>
        <summary class="find-summary">
          <span class="find-chev">
            <Icon name="chevron-right" />
          </span>
          <span class="card-title">Find documents</span>
          <span class="find-summary-text">{summaryOf(find)}</span>
        </summary>

        <div class="find-body">
          <Tabs id="mode" label="Filter mode" items={modes} value={find.mode} />

          <Options find={find} />

          <details class="find-preview" open data-fold="find-request">
            <summary class="label">
              <span class="find-chev">
                <Icon name="chevron-right" size={16} />
              </span>
              Request
            </summary>

            <div data-find-preview={request}>
              <CodeBlock code={code} />
            </div>
          </details>

          <div class="form-actions">
            <Button variant="dark" type="submit">
              Run
            </Button>

            <Button variant="ghost" href={docsHref(current.name, coll, undefined)}>
              Reset
            </Button>
          </div>
        </div>
      </details>

      {whereError && <p class="note bad">{whereError}</p>}
    </form>
  );
};

const RunStatus: FC<DocumentsProps> = ({ plan, examined, total, rows }) => {
  const parts = [total === undefined ? `${rows.length} returned` : `${total.toLocaleString('en-US')} ${total === 1 ? 'match' : 'matches'}`, plan ? `plan ${plan}` : undefined, examined === undefined ? undefined : `examined ${examined.toLocaleString('en-US')}`];

  return <p class="find-status">{parts.filter(Boolean).join(' · ')}</p>;
};

// PageSize is a small form in the table footer. It keeps the find query and
// goes back to the first page, because a cursor belongs to one page size.
const PageSize: FC<{ current: Current; coll: string; find: FindQuery }> = ({ current, coll, find }) => {
  const kept = [...findParams(find)].filter(([key]) => key !== 'limit');
  const limits = LIMITS.map(limit => ({ id: String(limit), label: `${limit} per page` }));

  return (
    <form class="pagination-size" method="get" action={`c/${encodeURIComponent(current.name)}/explorer`}>
      <input type="hidden" name="coll" value={coll} />
      <input type="hidden" name="tab" value="documents" />

      {kept.map(([key, value]) => (
        <input type="hidden" name={key} value={value} />
      ))}

      <Select name="limit" value={String(find.limit)} options={limits} aria-label="Page size" data-autosubmit />

      <noscript>
        <Button size="sm" type="submit">
          Apply
        </Button>
      </noscript>
    </form>
  );
};

// sortHref is the link of a column header: the first click sorts down, the
// next one sorts up. A new sort starts at the first page.
function sortHref(name: string, coll: string, find: FindQuery, column: string): string {
  const dir = find.sort === column && find.dir === 'desc' ? 'asc' : 'desc';

  return docsHref(name, coll, { ...find, sort: column, dir });
}

const Results: FC<DocumentsProps & { writable: boolean }> = props => {
  const { current, coll, find, rows, trail, id, total, next, writable, whereError } = props;

  const names = columnsOf(rows, 24);
  const columns = names.map(column => ({ id: column, label: column, href: sortHref(current.name, coll, find, column), sort: find.sort === column ? find.dir : undefined }));

  const page = trail.length + 1;
  const from = trail.length * find.limit + 1;
  const to = rows.length === 0 ? 0 : from + rows.length - 1;

  const firstHref = trail.length > 0 ? docsHref(current.name, coll, find) : undefined;
  const prevHref = trail.length > 0 ? docsHref(current.name, coll, find, { trail: trail.slice(0, -1) }) : undefined;
  const nextHref = next ? docsHref(current.name, coll, find, { trail: [...trail, next] }) : undefined;

  const title = <h2 class="card-title">Documents</h2>;

  const insert = writable ? (
    <Button size="sm" href={docsHref(current.name, coll, find, { trail, insert: true })} {...openDialog('insert')}>
      <Icon name="plus" />
      Insert
    </Button>
  ) : undefined;

  const empty = rows.length === 0 ? <Empty title="No documents" description={whereError ? 'Fix the filter and run it again.' : 'Nothing matches this filter.'} /> : undefined;

  const footer = (
    <Pagination page={page} from={from} to={to} total={total} firstHref={firstHref} prevHref={prevHref} nextHref={nextHref}>
      <PageSize current={current} coll={coll} find={find} />
    </Pagination>
  );

  return (
    <div class="card flush dtable-card">
      <DataTable id={`docs:${coll}`} label="Documents" columns={columns} minWidth={Math.max(480, columns.length * 140)} title={title} actions={insert} footer={footer} empty={empty}>
        {rows.map(doc => (
          <ResultRow current={current} coll={coll} find={find} trail={trail} doc={doc} columns={names} on={String(doc._id) === id} />
        ))}
      </DataTable>
    </div>
  );
};

const ResultRow: FC<{ current: Current; coll: string; find: FindQuery; trail: string[]; doc: Doc; columns: string[]; on: boolean }> = ({ current, coll, find, trail, doc, columns, on }) => {
  const docId = String(doc._id);
  const href = docsHref(current.name, coll, find, { trail, id: docId });

  return (
    <tr class={on ? 'find-row-on' : undefined}>
      {columns.map(column => (
        <td class="mono" data-col={column} title={column === '_id' ? undefined : cellText(doc[column], 400)}>
          {column === '_id' ? (
            <a class="strong" href={href}>
              {cellText(doc._id, 40)}
            </a>
          ) : (
            cellText(doc[column])
          )}
        </td>
      ))}
    </tr>
  );
};

const InsertDialog: FC<{ current: Current; coll: string; closeHref: string; open: boolean; error?: string }> = ({ current, coll, closeHref, open, error }) => (
  <Dialog id="insert" title="Insert a document" description={`A new document in ${coll}. Without _id, ndb makes one.`} open={open} closeHref={closeHref}>
    <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/browser/insert`} data-insert-form>
      <input type="hidden" name="coll" value={coll} />

      <Textarea mono name="doc" rows={10} {...noSpelling} aria-label="Document JSON" placeholder={'{\n  "status": "new"\n}'} required />

      <p class="note bad" data-insert-error hidden={!error}>
        {error}
      </p>

      <div class="form-actions">
        <Button variant="primary" type="submit">
          Insert
        </Button>
        <Button variant="ghost" href={closeHref} data-dialog-close>
          Cancel
        </Button>
      </div>
    </form>
  </Dialog>
);

const DocumentPane: FC<{ current: Current; coll: string; doc?: Doc; error?: string; writable: boolean; reason?: string }> = ({ current, coll, doc: found, error, writable, reason }) => {
  if (error) return <p class="note bad">{error}</p>;

  if (!found) return null;

  const doc = { _id: found._id, _v: found._v, ...found };
  const id = String(doc._id);
  const props = { coll, id, doc, saveUrl: `c/${encodeURIComponent(current.name)}/browser/save`, writable, reason };

  return (
    <>
      <div id="doc-editor" data-props={JSON.stringify(props)}>
        <div class="doc-head">
          <span class="doc-id mono">{id}</span>
          <span class="doc-meta">
            _v {String(doc._v)} · {byteSize(doc)} B
          </span>
        </div>

        <StaticTree value={doc} />

        {reason && <p class="hint">{reason}</p>}
      </div>

      {writable && (
        <form
          class="doc-delete"
          method="post"
          action={`c/${encodeURIComponent(current.name)}/browser/delete`}
          {...confirm({ title: 'Delete this document?', body: `${id} is removed from ${coll}. A watch consumer sees a del event.`, action: 'Delete' })}
        >
          <input type="hidden" name="coll" value={coll} />
          <input type="hidden" name="id" value={id} />

          <Button variant="destructive" size="sm" type="submit">
            Delete
          </Button>
        </form>
      )}
    </>
  );
};

// Documents is the Documents tab of the Explorer: the find form with its
// request preview, the run status, the results table, and the selected
// document in a drawer with the editor island.
export const Documents: FC<DocumentsProps> = props => {
  const { current, collections, coll, find, trail, id, selected, selectedError, notice, insertOpen, insertError, whereError } = props;

  const stats = collections.find(item => item.name === coll);
  const direct = stats?.write === 'direct';
  const writable = !direct;
  const reason = direct ? 'This collection is in direct mode: ndb reads the files of another app, so documents cannot be written here.' : undefined;

  const closeHref = docsHref(current.name, coll, find, { trail });
  const filtered = find.rows.some(row => row.field.trim() !== '') || find.json.trim() !== '' || find.fields !== '';
  const findForce = Boolean(whereError) || props.findOpen === true;
  const findOpen = findForce || filtered;
  const drawerOpen = Boolean(id && (selected || selectedError));

  return (
    <>
      <FindForm {...props} open={findOpen} force={findForce} />

      {direct && (
        <p class="hint">
          <Badge tone="pink">direct</Badge> {reason}
        </p>
      )}

      {notice && <p class="note ok">{notice}</p>}

      <RunStatus {...props} />

      <Results {...props} writable={writable} />

      <Drawer id="doc" title="Document" description={coll} open={drawerOpen} closeHref={closeHref}>
        <DocumentPane current={current} coll={coll} doc={selected} error={selectedError} writable={writable} reason={reason} />
      </Drawer>

      {writable && <InsertDialog current={current} coll={coll} closeHref={closeHref} open={Boolean(insertOpen)} error={insertError} />}

      <ConfirmHost />
    </>
  );
};
