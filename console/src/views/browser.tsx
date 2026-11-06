import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { StaticTree } from '@/components/json-tree';
import { byteSize } from '@/components/json-model';
import { formatCount } from '@/format';
import type { CollectionStats, Doc } from '@/ndb';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Dialog, openDialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Input } from '@/ui/input';
import { Textarea } from '@/ui/textarea';
import { noSpelling } from '@/ui/input';

export interface BrowserQuery {
  coll: string;
  where: string;
  id?: string;
  after?: string;
  q?: string;
}

export interface BrowserPageProps {
  current: Current;
  collections: CollectionStats[];
  query: BrowserQuery;
  rows: Doc[];
  plan?: string;
  examined?: number;
  total?: number;
  next?: string;
  whereError?: string;
  selected?: Doc;
  selectedError?: string;
  notice?: string;
  insertOpen?: boolean;
  insertError?: string;
}

// browserHref builds a link of the Browser that keeps the view in the URL.
export function browserHref(name: string, query: Partial<BrowserQuery> & { insert?: boolean; done?: string }): string {
  const params = new URLSearchParams();

  if (query.coll) params.set('coll', query.coll);

  if (query.where) params.set('where', query.where);

  if (query.after) params.set('after', query.after);

  if (query.id) params.set('id', query.id);

  if (query.insert) params.set('insert', '1');

  if (query.done) params.set('done', query.done);

  const text = params.toString();

  return `c/${encodeURIComponent(name)}/browser${text ? `?${text}` : ''}`;
}

function preview(doc: Doc): string {
  const { _id, _v, ...rest } = doc;

  return JSON.stringify(rest);
}

const Sidebar: FC<{ current: Current; collections: CollectionStats[]; query: BrowserQuery }> = ({ current, collections, query }) => {
  const needle = (query.q ?? '').toLowerCase();
  const shown = collections.filter(item => item.name.toLowerCase().includes(needle));

  return (
    <aside class="bsidebar" aria-label="Collections">
      <form class="bfilter" method="get" action={`c/${encodeURIComponent(current.name)}/browser`}>
        <input type="hidden" name="coll" value={query.coll} />

        <label class="label" for="coll-filter">
          Collections
        </label>
        <Input id="coll-filter" type="search" name="q" value={query.q ?? ''} placeholder="Filter" />
      </form>

      <nav class="bcols">
        {shown.map(item => (
          <a class={item.name === query.coll ? 'bcol on' : 'bcol'} href={browserHref(current.name, { coll: item.name })}>
            <span class="bcol-name mono">{item.name}</span>
            <span class="bcol-rows">{formatCount(item.rows)}</span>
          </a>
        ))}

        {shown.length === 0 && <p class="hint">No collection matches.</p>}
      </nav>
    </aside>
  );
};

const DocRow: FC<{ doc: Doc; href: string; on: boolean }> = ({ doc, href, on }) => (
  <a class={on ? 'docrow on' : 'docrow'} href={href} aria-current={on ? 'true' : undefined}>
    <span class="docrow-top mono">
      <span class="docrow-id">{String(doc._id)}</span>
      <span class="grow" />
      <span class="docrow-v">_v {String(doc._v ?? '')}</span>
    </span>

    <span class="docrow-preview mono">{preview(doc)}</span>
  </a>
);

const Rows: FC<BrowserPageProps & { selectedId?: string }> = ({ current, query, rows, plan, examined, total, next, whereError, selectedId }) => {
  const info = [total === undefined ? undefined : `${total.toLocaleString('en-US')} ${total === 1 ? 'match' : 'matches'}`, plan, examined === undefined ? undefined : `examined ${examined}`].filter(Boolean).join(' · ');

  return (
    <>
      <form class="wherebar" method="get" action={`c/${encodeURIComponent(current.name)}/browser`}>
        <input type="hidden" name="coll" value={query.coll} />

        <label class="wherebox">
          <span class="wherelabel">where</span>
          <input class="whereinput mono" type="text" name="where" value={query.where} placeholder='{"status":"pending"}' aria-label="Filter" {...noSpelling} />
        </label>

        <Button variant="dark" size="sm" type="submit">
          Run
        </Button>
      </form>

      {whereError && <p class="note bad">{whereError}</p>}

      {!whereError && (
        <div class="infoline">
          <span>{info}</span>
          <span class="grow" />

          {query.after && <a href={browserHref(current.name, { coll: query.coll, where: query.where })}>‹ First</a>}
          {next && <a href={browserHref(current.name, { coll: query.coll, where: query.where, after: next })}>Next ›</a>}
        </div>
      )}

      {!whereError && rows.length === 0 && (
        <div class="card">
          <Empty title="No documents" description={query.where ? 'Nothing matches this filter.' : 'This collection has no documents.'} />
        </div>
      )}

      {rows.length > 0 && (
        <div class="doclist">
          {rows.map(doc => (
            <DocRow doc={doc} on={String(doc._id) === selectedId} href={browserHref(current.name, { coll: query.coll, where: query.where, after: query.after, id: String(doc._id) })} />
          ))}
        </div>
      )}
    </>
  );
};

const InsertDialog: FC<{ current: Current; query: BrowserQuery; open: boolean; error?: string }> = ({ current, query, open, error }) => (
  <Dialog id="insert" title="Insert a document" description={`A new document in ${query.coll}. Without _id, ndb makes one.`} open={open} closeHref={browserHref(current.name, query)}>
    <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/browser/insert`} data-insert-form>
      <input type="hidden" name="coll" value={query.coll} />

      <Textarea mono name="doc" rows={10} {...noSpelling} aria-label="Document JSON" placeholder={'{\n  "status": "new"\n}'} required />

      <p class="note bad" data-insert-error hidden={!error}>
        {error}
      </p>

      <div class="form-actions">
        <Button variant="primary" type="submit">
          Insert
        </Button>
        <Button variant="ghost" href={browserHref(current.name, query)} data-dialog-close>
          Cancel
        </Button>
      </div>
    </form>
  </Dialog>
);

const DocumentPane: FC<{ current: Current; query: BrowserQuery; doc?: Doc; error?: string; writable: boolean; reason?: string }> = ({ current, query, doc: found, error, writable, reason }) => {
  if (error) return <p class="note bad">{error}</p>;

  if (!found) return <Empty title="Select a document" description="Pick a row to see its fields." />;

  const doc = { _id: found._id, _v: found._v, ...found };
  const id = String(doc._id);
  const props = { coll: query.coll, id, doc, saveUrl: `c/${encodeURIComponent(current.name)}/browser/save`, writable, reason };

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
          {...confirm({ title: 'Delete this document?', body: `${id} is removed from ${query.coll}. A watch consumer sees a del event.`, action: 'Delete' })}
        >
          <input type="hidden" name="coll" value={query.coll} />
          <input type="hidden" name="id" value={id} />

          <Button variant="destructive" size="sm" type="submit">
            Delete
          </Button>
        </form>
      )}
    </>
  );
};

export const BrowserPage: FC<BrowserPageProps> = props => {
  const { current, collections, query, selected, selectedError, notice, insertOpen, insertError } = props;

  const stats = collections.find(item => item.name === query.coll);
  const direct = stats?.write === 'direct';
  const writable = !direct;
  const reason = direct ? 'This collection is in direct mode: ndb reads the files of another app, so documents cannot be written here.' : undefined;

  return (
    <Layout title="Browser" current={current} tab="browser" scripts={['document-editor']} bleed>
      <div class="browser">
        <Sidebar current={current} collections={collections} query={query} />

        <section class="bmiddle" aria-label="Documents">
          <div class="bhead">
            <h1 class="btitle mono">{query.coll || 'No collection'}</h1>

            {stats && <Badge tone={direct ? 'pink' : 'neutral'}>{stats.write}</Badge>}
            {stats && <span class="hint">{stats.rows.toLocaleString('en-US')} rows</span>}

            <span class="grow" />

            {writable && stats && (
              <Button size="sm" href={browserHref(current.name, { ...query, insert: true })} {...openDialog('insert')}>
                + Insert
              </Button>
            )}
          </div>

          {direct && <p class="hint">{reason}</p>}

          {notice && <p class="note ok">{notice}</p>}

          {stats ? <Rows {...props} selectedId={selected ? String(selected._id) : query.id} /> : <Empty title="No collection yet" description="Insert a document and the collection appears here." />}
        </section>

        <section class="bright" aria-label="Document">
          <DocumentPane current={current} query={query} doc={selected} error={selectedError} writable={writable} reason={reason} />
        </section>
      </div>

      {writable && stats && <InsertDialog current={current} query={query} open={Boolean(insertOpen)} error={insertError} />}

      <ConfirmHost />
    </Layout>
  );
};
