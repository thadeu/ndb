import type { FC } from 'hono/jsx';

import type { Current } from '@/html/layout';
import { DocFacts } from '@/components/document-facts';
import { StaticTree } from '@/components/json-tree';
import { byteSize } from '@/components/json-model';
import { cellText, columnsOf, type FilterRow, type FindQuery, findParams, DEFAULT_LIMIT, DEFAULT_PER_LIMIT, LIMITS, needsValue, OPERATORS } from '@/components/filter-model';
import { heldLock, lockKey } from '@/locks';
import type { CollectionStats, Doc } from '@/ndb';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Dialog, openDialog } from '@/ui/dialog';
import { DataTable, RowCheck } from '@/ui/data-table';
import { Drawer } from '@/ui/drawer';
import { Empty } from '@/ui/empty';
import { Code } from '@/ui/code';
import { Icon } from '@/ui/icon';
import { Input, noSpelling } from '@/ui/input';
import { Pagination } from '@/ui/pagination';
import { Select } from '@/ui/select';
import { Textarea } from '@/ui/textarea';
import { ToggleGroup } from '@/ui/toggle-group';

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
  problem?: string;
  insertOpen?: boolean;
  insertError?: string;
  findOpen?: boolean;
}

export interface DocsLink {
  trail?: string[];
  id?: string;
  insert?: boolean;
  done?: string;
  count?: number;
  fail?: string;
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

  if (link.count !== undefined) params.set('n', String(link.count));

  if (link.fail) params.set('fail', link.fail);

  return `c/${encodeURIComponent(name)}/explorer?${params}`;
}

// exportHref is the link of Export NDJSON: the find query of the page, with
// no cursor, so the file starts at the first match.
export function exportHref(name: string, coll: string, find: FindQuery): string {
  const params = new URLSearchParams({ coll });

  for (const [key, value] of findParams(find)) params.append(key, value);

  return `c/${encodeURIComponent(name)}/browser/export?${params}`;
}

// browserHref is the short form for the routes that only know a collection,
// such as the redirect of the old Browser page and the write routes.
export function browserHref(name: string, query: { coll: string; where?: string; id?: string; after?: string; done?: string; insert?: boolean }): string {
  const find = query.where ? { mode: 'json' as const, rows: [], json: query.where, sort: '', dir: 'desc' as const, fields: '', limit: DEFAULT_LIMIT, per: '', perLimit: DEFAULT_PER_LIMIT } : undefined;

  return docsHref(name, query.coll, find, { ...query, trail: query.after ? [query.after] : [] });
}

const BULK_FORM = 'docs-bulk';

// DOC_DRAWER is the id of the bottom drawer of the selected document. The
// script of the rows (document-drawer.ts) opens it without a page load.
export const DOC_DRAWER = 'doc';

const MODES = [
  { id: 'builder', label: 'Builder' },
  { id: 'json', label: 'JSON' },
];

const DIRECTIONS = [
  { id: 'desc', label: '↓ Descending' },
  { id: 'asc', label: '↑ Ascending' },
];

function base(current: Current): string {
  return `c/${encodeURIComponent(current.name)}`;
}

const FilterRowFields: FC<{ row: FilterRow; index: number; only: boolean }> = ({ row, index, only }) => {
  const hint = needsValue(row.op) ? 'Value, such as paid or 10' : 'No value';

  return (
    <div class="find-row" data-row={String(index)}>
      <Input mono name="f" value={row.field} placeholder="Field, such as status" aria-label="Field name" />

      <Select name="op" value={row.op} options={OPERATORS} aria-label="Condition" />

      <input type="hidden" name="t" value={row.type} />

      <Input mono name="v" value={row.value} placeholder={hint} aria-label="Value" disabled={!needsValue(row.op)} />

      {only ? (
        <span class="find-row-x" />
      ) : (
        <Button
          variant="ghost"
          size="sm"
          class="icon-only find-row-x"
          type="submit"
          name="remove"
          value={String(index)}
          aria-label="Remove this condition"
        >
          <Icon name="x" />
        </Button>
      )}
    </div>
  );
};

const FilterRows: FC<{ rows: FilterRow[] }> = ({ rows }) => (
  <div class="find-rows">
    {rows.map((row, index) => (
      <FilterRowFields row={row} index={index} only={rows.length === 1} />
    ))}

    <div class="find-rows-foot">
      <Button variant="ghost" size="sm" type="submit" name="add" value="1">
        <Icon name="plus" />
        Add condition
      </Button>

      <span class="hint">All conditions must match. Numbers, true, false and null are read as such; quotes keep a text.</span>
    </div>
  </div>
);

const JsonFilter: FC<{ json: string }> = ({ json }) => (
  <div class="find-json">
    <p class="hint">A MongoDB filter, such as {'{"status":"paid","total":{"$gte":10}}'}.</p>

    <Textarea mono name="where" rows={4} placeholder='{"status":"pending"}' aria-label="Filter JSON" {...noSpelling}>
      {json}
    </Textarea>
  </div>
);

const Options: FC<{ find: FindQuery }> = ({ find }) => (
  <div class="find-options">
    <div class="find-option">
      <label for="find-sort">Sort by</label>
      <Input mono id="find-sort" name="sort" value={find.sort} placeholder="none" />
      <Select name="dir" value={find.dir} options={DIRECTIONS} aria-label="Sort order" />
    </div>

    <div class="find-option">
      <label for="find-fields">Fields</label>
      <Input mono id="find-fields" name="fields" value={find.fields} placeholder="all" />
    </div>

    <div class="find-option" title="The first N rows of each value of a field">
      <label for="find-per">Per value of</label>
      <Input mono id="find-per" name="per" value={find.per} placeholder="off" />
      <Input class="find-num" type="number" min="1" name="per_limit" value={String(find.perLimit)} aria-label="Rows of each value" />
    </div>
  </div>
);

const RequestPanel: FC<{ coll: string; body: Record<string, unknown> }> = ({ coll, body }) => {
  const request = `POST /${coll}/_search`;
  const code = `${request}\n${JSON.stringify(body, null, 2)}`;
  const line = `${request} ${JSON.stringify(body)}`;

  return (
    <details class="find-request">
      <summary class="btn small ghost">
        <Icon name="code" />
        Request
      </summary>

      <div class="find-request-panel" data-find-preview={request}>
        <div class="find-request-head">
          <span class="hint">What a run sends to ndb</span>

          <button type="button" class="btn small" data-copy={line}>
            Copy
          </button>
        </div>

        <pre class="mono">
          <Code text={code} />
        </pre>
      </div>
    </details>
  );
};

// FindBar is the find query of the page as one form: the conditions as
// rows (field, condition, value) or a JSON filter, then sort, fields and
// per, then the request that a run sends, Reset and Run. Every part is
// always in view, and plain CSS switches the mode, so it works without the
// script.
const FindBar: FC<DocumentsProps> = ({ current, coll, find, body, whereError }) => (
  <form class="find" method="get" action={`${base(current)}/explorer`} aria-label="Find documents" data-find-form>
    <input type="hidden" name="coll" value={coll} />
    <input type="hidden" name="tab" value="documents" />
    <input type="hidden" name="limit" value={String(find.limit)} />

    <div class="find-head">
      <h2 class="card-title">Filter</h2>

      <ToggleGroup name="mode" label="Filter mode" items={MODES} value={find.mode} />
    </div>

    <FilterRows rows={find.rows} />

    <JsonFilter json={find.json} />

    {whereError && <p class="note bad">{whereError}</p>}

    <Options find={find} />

    <div class="find-foot">
      <RequestPanel coll={coll} body={body} />

      <span class="grow" />

      <Button variant="ghost" href={docsHref(current.name, coll, undefined)}>
        Reset
      </Button>

      <Button variant="dark" type="submit">
        Run
      </Button>
    </div>
  </form>
);

const RunStatus: FC<DocumentsProps> = ({ plan, examined, total, rows }) => {
  const parts = [total === undefined ? `${rows.length} returned` : `${total.toLocaleString('en-US')} ${total === 1 ? 'match' : 'matches'}`, plan ? `plan ${plan}` : undefined, examined === undefined ? undefined : `examined ${examined.toLocaleString('en-US')}`];

  return <span class="find-status">{parts.filter(Boolean).join(' · ')}</span>;
};

// PageSize is a small form in the table footer. It keeps the find query and
// goes back to the first page, because a cursor belongs to one page size.
const PageSize: FC<{ current: Current; coll: string; find: FindQuery }> = ({ current, coll, find }) => {
  const kept = [...findParams(find)].filter(([key]) => key !== 'limit');
  const limits = LIMITS.map(limit => ({ id: String(limit), label: `${limit} per page` }));

  return (
    <form class="pagination-size" method="get" action={`${base(current)}/explorer`}>
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

// BulkBar is the form of the selected rows. The checkboxes of the table
// belong to it through their form attribute.
const BulkBar: FC<{ current: Current; coll: string; find: FindQuery }> = ({ current, coll, find }) => {
  const ask = confirm({ title: 'Delete the selected documents?', body: `They are removed from ${coll}. A watch consumer sees a del event for each one.`, action: 'Delete' });

  return (
    <form
      id={BULK_FORM}
      class="dtable-bulk-form"
      method="post"
      action={`${base(current)}/browser/delete-many`}
      {...ask}
    >
      <input type="hidden" name="coll" value={coll} />
      <input type="hidden" name="find" value={findParams(find).toString()} />

      <span class="dtable-bulk-text">Selected rows</span>

      <Button variant="destructive" size="sm" type="submit">
        Delete <span data-bulk-count>selected</span>
      </Button>
    </form>
  );
};

const Results: FC<DocumentsProps & { writable: boolean }> = props => {
  const { current, coll, find, rows, trail, id, total, next, writable, whereError } = props;

  const names = columnsOf(rows, 24);
  const sortable = names.map(column => ({ id: column, label: column, href: sortHref(current.name, coll, find, column), sort: find.sort === column ? find.dir : undefined }));
  const columns = [...sortable, { id: '_v', label: '_v', align: 'right' as const }];

  const page = trail.length + 1;
  const from = trail.length * find.limit + 1;
  const to = rows.length === 0 ? 0 : from + rows.length - 1;

  const firstHref = trail.length > 0 ? docsHref(current.name, coll, find) : undefined;
  const prevHref = trail.length > 0 ? docsHref(current.name, coll, find, { trail: trail.slice(0, -1) }) : undefined;
  const nextHref = next ? docsHref(current.name, coll, find, { trail: [...trail, next] }) : undefined;

  const title = (
    <>
      <h2 class="card-title">Documents</h2>
      <RunStatus {...props} />
    </>
  );

  const actions = (
    <>
      {rows.length > 0 && (
        <Button size="sm" href={exportHref(current.name, coll, find)} download>
          Export NDJSON
        </Button>
      )}

      {writable && (
        <Button variant="primary" size="sm" href={docsHref(current.name, coll, find, { trail, insert: true })} {...openDialog('insert')}>
          <Icon name="plus" />
          Insert
        </Button>
      )}
    </>
  );

  const empty = rows.length === 0 ? <Empty title="No documents" description={whereError ? 'Fix the filter and run it again.' : 'Nothing matches this filter.'} /> : undefined;
  const bulk = writable ? <BulkBar current={current} coll={coll} find={find} /> : undefined;

  const footer = (
    <Pagination page={page} from={from} to={to} total={find.per ? undefined : total} firstHref={firstHref} prevHref={prevHref} nextHref={nextHref}>
      {find.per ? <span class="pagination-range">first {find.perLimit} of each {find.per}</span> : <PageSize current={current} coll={coll} find={find} />}
    </Pagination>
  );

  return (
    <div class="card flush dtable-card">
      <DataTable
        id={`docs:${coll}`}
        label="Documents"
        columns={columns}
        minWidth={Math.max(480, columns.length * 140)}
        title={title}
        actions={actions}
        footer={footer}
        empty={empty}
        selectable={writable}
        bulk={bulk}
      >
        {rows.map(doc => (
          <ResultRow current={current} coll={coll} find={find} trail={trail} doc={doc} columns={names} on={String(doc._id) === id} selectable={writable} />
        ))}
      </DataTable>
    </div>
  );
};

const ResultRow: FC<{ current: Current; coll: string; find: FindQuery; trail: string[]; doc: Doc; columns: string[]; on: boolean; selectable: boolean }> = ({ current, coll, find, trail, doc, columns, on, selectable }) => {
  const docId = String(doc._id);
  const href = docsHref(current.name, coll, find, { trail, id: docId });

  return (
    <tr class={on ? 'find-row-on' : undefined} data-doc-href={href}>
      {selectable && <RowCheck name="ids" value={docId} form={BULK_FORM} label={`Select ${cellText(doc._id, 40)}`} />}

      {columns.map(column => (
        <td class="mono" data-col={column} title={column === '_id' ? undefined : cellText(doc[column], 400)}>
          {column === '_id' ? (
            <a class="strong" href={href} data-doc-link>
              {cellText(doc._id, 40)}
            </a>
          ) : (
            cellText(doc[column])
          )}
        </td>
      ))}

      <td class="mono num dtable-version" data-col="_v">
        {cellText(doc._v)}
      </td>
    </tr>
  );
};

const InsertDialog: FC<{ current: Current; coll: string; closeHref: string; open: boolean; error?: string }> = ({ current, coll, closeHref, open, error }) => (
  <Dialog id="insert" title="Insert a document" description={`A new document in ${coll}. Without _id, ndb makes one.`} open={open} closeHref={closeHref}>
    <form class="stack" method="post" action={`${base(current)}/browser/insert`} data-insert-form>
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

// utcTime is the clock time of an RFC 3339 instant. The server does not
// know the time zone of the reader; the editor island shows the local time.
function utcTime(instant: string): string {
  const date = new Date(instant);

  return Number.isNaN(date.getTime()) ? instant : `${date.toISOString().slice(11, 19)} UTC`;
}

// LockForm is the lock row without the script: a form that takes or
// releases the lock and comes back to this page.
const LockForm: FC<{ action: string; coll: string; id: string; find: string; until?: string }> = ({ action, coll, id, find, until }) => (
  <form class="doc-lock" method="post" action={action}>
    <input type="hidden" name="coll" value={coll} />
    <input type="hidden" name="id" value={id} />
    <input type="hidden" name="find" value={find} />

    {until ? (
      <span class="doc-lock-text">
        <strong>Locked by this console</strong> until {utcTime(until)}.
      </span>
    ) : (
      <span class="doc-lock-text">
        <strong>Not locked.</strong> A save checks <code class="mono">_v</code> (optimistic).
      </span>
    )}

    <Button size="sm" type="submit">
      {until ? 'Unlock' : 'Lock for 30 s'}
    </Button>
  </form>
);

interface PaneProps {
  current: Current;
  coll: string;
  find: FindQuery;
  id: string;
  doc?: Doc;
  error?: string;
  writable: boolean;
  reason?: string;
  notice?: string;
  problem?: string;
}

// DocumentPane is the selected document before the editor island starts, in
// two columns: the fields as a tree that cannot be edited, and at the side
// the facts of the document with the lock and delete forms. The island reads
// data-props and takes the place of all of it.
const DocumentPane: FC<PaneProps> = ({ current, coll, find, id: wanted, doc: found, error, writable, reason, notice, problem }) => {
  if (error || !found) {
    return (
      <div class="doc-missing">
        <span class="doc-id mono">{wanted}</span>

        <p class="note bad">{error ?? 'The document was not found.'}</p>
      </div>
    );
  }

  const doc = { _id: found._id, _v: found._v, ...found };
  const id = String(doc._id);
  const query = findParams(find).toString();
  const lock = heldLock(lockKey(current.url, coll, id));

  const urls = { saveUrl: `${base(current)}/browser/save`, lockUrl: `${base(current)}/browser/lock`, unlockUrl: `${base(current)}/browser/unlock`, deleteUrl: `${base(current)}/browser/delete` };
  const props = { coll, id, doc, ...urls, find: query, lockedUntil: lock?.until, writable, reason, notice, problem };
  const ask = confirm({ title: 'Delete this document?', body: `${id} is removed from ${coll}. A watch consumer sees a del event.`, action: 'Delete' });

  return (
    <div id="doc-editor" class="doc-editor" data-props={JSON.stringify(props)}>
      <section class="doc-fields" aria-label="Fields">
        <StaticTree value={doc} />
      </section>

      <div class="doc-side">
        <DocFacts id={id} version={String(doc._v)} bytes={byteSize(doc)} />

        {writable && <LockForm action={lock ? urls.unlockUrl : urls.lockUrl} coll={coll} id={id} find={query} until={lock?.until} />}

        {notice && <p class="note ok">{notice}</p>}

        {problem && <p class="note bad">{problem}</p>}

        {reason && <p class="hint">{reason}</p>}

        {writable && (
          <form class="doc-actions" method="post" action={urls.deleteUrl} {...ask}>
            <input type="hidden" name="coll" value={coll} />
            <input type="hidden" name="id" value={id} />
            <input type="hidden" name="find" value={query} />

            <Button variant="destructive" type="submit">
              Delete
            </Button>
          </form>
        )}
      </div>
    </div>
  );
};

// DocList is the list at the left of the drawer: the documents of this
// page, so the reader goes from one to the next and the drawer stays open.
const DocList: FC<{ current: Current; coll: string; find: FindQuery; trail: string[]; rows: Doc[]; id?: string }> = ({ current, coll, find, trail, rows, id }) => (
  <nav class="doc-list" aria-label="Documents of this page">
    <div class="doc-list-head">
      <Input class="doc-list-filter" type="search" placeholder="Filter this page" aria-label="Filter the documents of this page" data-doc-list-filter />

      <span class="doc-list-count">
        {rows.length} {rows.length === 1 ? 'document' : 'documents'}
      </span>
    </div>

    <ul class="doc-items">
      {rows.map(doc => (
        <DocListItem doc={doc} href={docsHref(current.name, coll, find, { trail, id: String(doc._id) })} on={String(doc._id) === id} />
      ))}
    </ul>
  </nav>
);

const DocListItem: FC<{ doc: Doc; href: string; on: boolean }> = ({ doc, href, on }) => {
  const { _id, _v, ...rest } = doc;
  const className = on ? 'doc-item find-row-on' : 'doc-item';

  return (
    <li>
      <a
        class={className}
        href={href}
        data-doc-href={href}
        data-doc-link
        aria-current={on ? 'true' : undefined}
        autofocus={on}
      >
        <span class="doc-item-top mono">
          <span class="doc-item-id">{cellText(_id, 48)}</span>
          <span class="doc-item-v">_v {cellText(_v)}</span>
        </span>

        <span class="doc-item-preview mono">
          <Code text={cellText(rest, 120)} />
        </span>
      </a>
    </li>
  );
};

// Documents is the Documents tab of the Explorer: the find bar with its
// request, the results table with the bulk actions, and the selected
// document in a drawer that comes up from the bottom, with the editor
// island. The drawer has the list of the page at its left, so the next
// document opens with no need to close it. The drawer is always in the
// page, so a click on a row can fill it without a page load.
export const Documents: FC<DocumentsProps> = props => {
  const { current, collections, coll, find, trail, id, selected, selectedError, notice, problem, insertOpen, insertError, whereError } = props;

  const stats = collections.find(item => item.name === coll);
  const direct = stats?.write === 'direct';
  const writable = !direct;
  const reason = direct ? 'This collection is in direct mode: ndb reads the files of another app, so documents cannot be written here.' : undefined;

  const closeHref = docsHref(current.name, coll, find, { trail });
  const drawerOpen = Boolean(id && (selected || selectedError));

  return (
    <>
      <FindBar {...props} />

      {direct && (
        <p class="hint">
          <Badge tone="pink">direct</Badge> {reason}
        </p>
      )}

      {notice && <p class="note ok">{notice}</p>}

      {problem && <p class="note bad">{problem}</p>}

      <Results {...props} writable={writable} />

      <Drawer
        id={DOC_DRAWER}
        side="bottom"
        class="doc-drawer"
        title="Document"
        description={`In ${coll}`}
        open={drawerOpen}
        closeHref={closeHref}
      >
        <div class="doc-shell">
          <DocList current={current} coll={coll} find={find} trail={trail} rows={props.rows} id={id} />

          <div class="doc-pane">
            {drawerOpen && id && (
              <DocumentPane
                current={current}
                coll={coll}
                find={find}
                id={id}
                doc={selected}
                error={selectedError}
                writable={writable}
                reason={reason}
                notice={notice}
                problem={problem}
              />
            )}
          </div>
        </div>
      </Drawer>

      {writable && <InsertDialog current={current} coll={coll} closeHref={closeHref} open={Boolean(insertOpen)} error={insertError} />}

      <ConfirmHost />
    </>
  );
};
