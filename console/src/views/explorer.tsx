import type { FC } from 'hono/jsx';

import { COLLECTION_NAME, NO_KEY_ROUTE, type CollectionDefinition, type CollectionInfo, type Explain, type IndexRow, type QuarantineRow, indexRequest, indexRows, planText, requestText } from '@/collections';
import { Documents, type DocumentsProps } from '@/components/documents';
import { RestoreConfirm, RestoreFields, WindowBar, copyName, type RestoreDraft } from '@/components/restore-form';
import { Layout, type Current } from '@/html/layout';
import { formatBytes, formatCount } from '@/format';
import type { PitrInfo, Stats } from '@/ndb';
import { seconds, when } from '@/pitr';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { DescriptionList } from '@/ui/description-list';
import { Dialog, openDialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Icon } from '@/ui/icon';
import { Input, noSpelling } from '@/ui/input';
import { Menu, MenuButton, MenuItem } from '@/ui/menu';
import { type Segment, SegmentBar } from '@/ui/progress';
import { Select } from '@/ui/select';
import { Table } from '@/ui/table';
import { Textarea } from '@/ui/textarea';

export type ExplorerTab = 'overview' | 'documents' | 'indexes' | 'storage' | 'locks' | 'backups';
export type ExplorerShow = 'all' | 'favorites' | 'streams';
export type ExplorerDialog = 'new' | 'edit' | 'rename';

export interface ExplorerQuery {
  coll?: string;
  tab: ExplorerTab;
  find?: string;
  show: ExplorerShow;
  dialog?: ExplorerDialog;
  filter?: string;
}

export interface ExplainResult {
  result?: Explain;
  error?: string;
}

export interface ExplorerPageProps {
  current: Current;
  stats: Stats;
  definitions: CollectionDefinition[];
  query: ExplorerQuery;
  matched?: string[];
  findError?: string;
  pitr?: PitrInfo;
  documents?: Omit<DocumentsProps, 'current'>;
  explain?: ExplainResult;
  quarantine?: QuarantineRow[];
  restore?: RestoreDraft;
  restored?: string;
  updated: string;
  notice?: string;
  error?: string;
}

type DetailProps = ExplorerPageProps & { collection: CollectionInfo; definition?: CollectionDefinition };

const TABS: { id: ExplorerTab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'documents', label: 'Documents' },
  { id: 'indexes', label: 'Indexes' },
  { id: 'storage', label: 'Storage' },
  { id: 'locks', label: 'Locks' },
  { id: 'backups', label: 'Backups' },
];

const SHOWS: { id: ExplorerShow; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'favorites', label: 'Favorites' },
  { id: 'streams', label: 'Streams' },
];

function base(current: Current): string {
  return `c/${encodeURIComponent(current.name)}`;
}

function listHref(current: Current, query: ExplorerQuery, changes: Partial<ExplorerQuery> = {}): string {
  const next = { ...query, ...changes };
  const params = new URLSearchParams();

  if (next.coll) params.set('coll', next.coll);

  if (next.coll && next.tab !== 'overview') params.set('tab', next.tab);

  if (next.find) params.set('find', next.find);

  if (next.show !== 'all') params.set('show', next.show);

  if (next.dialog) params.set('dialog', next.dialog);

  const text = params.toString();

  return text ? `${base(current)}/explorer?${text}` : `${base(current)}/explorer`;
}

function pageHref(current: Current, query: ExplorerQuery, changes: Partial<ExplorerQuery> = {}): string {
  return listHref(current, { ...query, dialog: undefined }, changes);
}

function actionUrl(current: Current, action: string): string {
  return `${base(current)}/explorer/${action}`;
}

export const ExplorerPage: FC<ExplorerPageProps> = props => {
  const { current, stats, definitions, query } = props;

  const streams = new Set(definitions.filter(definition => definition.stream).map(definition => definition.name));
  const find = (query.find ?? '').toLowerCase();
  const matched = props.matched ? new Set(props.matched) : undefined;

  const listed = stats.collections
    .filter(collection => (matched ? matched.has(collection.name) : collection.name.toLowerCase().includes(find)))
    .filter(collection => query.show !== 'streams' || streams.has(collection.name));

  const selected = stats.collections.find(collection => collection.name === query.coll);
  const definition = definitions.find(item => item.name === query.coll);
  const scripts = props.documents ? ['document-editor'] : [];

  return (
    <Layout title="Explorer" current={current} tab="explorer" scripts={scripts} bleed>
      <div class={selected ? 'explorer has-coll' : 'explorer'}>
        <CollectionList
          current={current}
          listed={listed}
          streams={streams}
          total={stats.collections.length}
          query={query}
          findError={props.findError}
        />

        <section class="explorer-main">
          {props.notice && (
            <p class="note ok">
              {props.notice} {props.restored && <a href={pageHref(current, query, { coll: props.restored, tab: 'documents' })}>Open {props.restored} in the Explorer</a>}
            </p>
          )}

          {props.error && <p class="note bad">{props.error}</p>}

          {selected ? (
            <Detail {...props} collection={selected} definition={definition} />
          ) : (
            <Empty title="Select a collection" description="Pick a collection in the list to see its details." />
          )}
        </section>
      </div>

      <NewCollectionDialog current={current} query={query} />
      <ConfirmHost />
    </Layout>
  );
};

interface CollectionListProps {
  current: Current;
  listed: CollectionInfo[];
  streams: Set<string>;
  total: number;
  query: ExplorerQuery;
  findError?: string;
}

const CollectionList: FC<CollectionListProps> = ({ current, listed, streams, total, query, findError }) => (
  <aside class="explorer-list" aria-label="Collections" data-favorites={current.name} data-show={query.show}>
    <div class="explorer-list-head">
      <h2 class="explorer-list-title">
        Collections <span class="explorer-list-count">({total})</span>
      </h2>

      <Button
        class="icon-only explorer-list-fold"
        variant="ghost"
        size="sm"
        type="button"
        data-collapse="list"
        aria-label="Fold or open the collection list"
      >
        <Icon name="sidebar" />
      </Button>

      <Button
        class="icon-only explorer-list-new"
        href={listHref(current, query, { dialog: 'new' })}
        aria-label="New collection"
        {...openDialog('new-collection')}
      >
        <Icon name="plus" />
      </Button>
    </div>

    <form class="explorer-find" method="get" action={`${base(current)}/explorer`} role="search">
      {query.coll && <input type="hidden" name="coll" value={query.coll} />}
      {query.coll && <input type="hidden" name="tab" value={query.tab} />}
      {query.show !== 'all' && <input type="hidden" name="show" value={query.show} />}

      <Icon name="search" size={16} />

      <input
        class="explorer-find-input"
        type="search"
        name="find"
        value={query.find ?? ''}
        placeholder="Find, or a pattern: logs-2026-*"
        aria-label="Find collections"
        {...noSpelling}
      />
    </form>

    <nav class="explorer-shows" aria-label="Show">
      {SHOWS.map(show => (
        <a
          class={show.id === query.show ? 'explorer-show on' : 'explorer-show'}
          href={listHref(current, query, { show: show.id })}
          aria-current={show.id === query.show ? 'true' : undefined}
        >
          {show.label}
        </a>
      ))}
    </nav>

    {findError && <p class="hint bad">{findError}</p>}

    <ul class="explorer-items">
      {listed.map(collection => (
        <CollectionItem current={current} collection={collection} stream={streams.has(collection.name)} query={query} />
      ))}
    </ul>

    {listed.length === 0 && <p class="hint">No collection matches.</p>}

    <p class="hint" data-favorites-empty hidden>
      No favorite yet. Use the star of a collection.
    </p>
  </aside>
);

const CollectionItem: FC<{ current: Current; collection: CollectionInfo; stream: boolean; query: ExplorerQuery }> = ({ current, collection, stream, query }) => {
  const on = collection.name === query.coll;
  const className = on ? 'explorer-item on' : 'explorer-item';
  const href = stream ? `${base(current)}/streams?stream=${encodeURIComponent(collection.name)}` : pageHref(current, query, { coll: collection.name });

  return (
    <li class={className} data-collection={collection.name}>
      <button
        type="button"
        class="explorer-star"
        data-favorite={collection.name}
        aria-label={`Favorite ${collection.name}`}
        aria-pressed="false"
        hidden
      >
        <Icon name="star" size={16} />
      </button>

      <a class="explorer-item-link" href={href} aria-current={on ? 'page' : undefined}>
        <span class="mono explorer-item-name">{collection.name}</span>
        {stream ? <span class="explorer-item-kind">stream</span> : <span class="explorer-item-count">{formatCount(collection.rows)}</span>}
      </a>
    </li>
  );
};

const NewCollectionDialog: FC<{ current: Current; query: ExplorerQuery }> = ({ current, query }) => {
  const writes = [
    { id: 'engine', label: 'engine (SDK writes)' },
    { id: 'direct', label: 'direct (apps append files)' },
  ];

  return (
    <Dialog
      id="new-collection"
      title="New collection"
      description="ndb sends PUT /{name} with this definition."
      open={query.dialog === 'new'}
      closeHref={pageHref(current, query)}
    >
      <form class="explorer-form" method="post" action={actionUrl(current, 'create')}>
        <Field id="new-name" label="Name">
          <Input id="new-name" name="collection" mono required pattern="[a-z0-9][a-z0-9_\-]{0,63}" placeholder="orders" />
        </Field>

        <Field id="new-write" label="Write mode">
          <Select id="new-write" name="write" options={writes} value="engine" />
        </Field>

        <Field id="new-path" label="Path (direct mode only: a glob of the NDJSON files)">
          <Input id="new-path" name="path" mono placeholder="/var/log/app/*.ndjson" />
        </Field>

        <Field id="new-time" label="Time field">
          <Input id="new-time" name="time" mono placeholder="created_at" />
        </Field>

        <Field id="new-indexes" label="Indexes (one in each line; a,b is one compound index)">
          <Textarea id="new-indexes" name="indexes" mono rows={3} placeholder={'status\naccount_id,created_at'} />
        </Field>

        <Field id="new-unique" label="Unique keys (one in each line)">
          <Textarea id="new-unique" name="unique" mono rows={2} placeholder="account_id,email" />
        </Field>

        <Field id="new-ttl" label="TTL (needs the time field)">
          <Input id="new-ttl" name="ttl" mono placeholder="30d" />
        </Field>

        <div class="form-actions">
          <Button variant="dark">Create collection</Button>
        </div>
      </form>
    </Dialog>
  );
};

function healthOf(collection: CollectionInfo): { ok: boolean; label: string } {
  if (collection.quarantined > 0) return { ok: false, label: `${formatCount(collection.quarantined)} quarantined` };

  if (collection.indexes.some(index => index.state !== 'ready')) return { ok: false, label: 'building' };

  return { ok: true, label: 'healthy' };
}

const Detail: FC<DetailProps> = props => {
  const { current, collection, definition, query } = props;

  return (
    <>
      <CollectionHead {...props} />

      {query.tab === 'overview' && <OverviewTab {...props} />}
      {query.tab === 'documents' && props.documents && <Documents current={current} {...props.documents} />}
      {query.tab === 'indexes' && <IndexesTab {...props} />}
      {query.tab === 'storage' && <StorageTab collection={collection} pitr={props.pitr} quarantine={props.quarantine} />}
      {query.tab === 'locks' && <LocksTab name={collection.name} />}
      {query.tab === 'backups' && <BackupsTab current={current} name={collection.name} pitr={props.pitr} draft={props.restore} />}

      <RenameDialog current={current} query={query} name={collection.name} />
      {definition && <EditDefinitionDialog current={current} query={query} definition={definition} />}
    </>
  );
};

const CollectionHead: FC<DetailProps> = ({ current, collection, definition, query, updated }) => {
  const health = healthOf(collection);

  return (
    <>
      <header class="explorer-head">
        <div class="explorer-head-names">
          <nav class="explorer-crumbs" aria-label="Breadcrumb">
            <a class="explorer-back" href={pageHref(current, query, { coll: undefined })}>
              <Icon name="arrow-left" size={16} />
              Collections
            </a>

            <span class="explorer-crumb-name">/ {collection.name}</span>
          </nav>

          <div class="explorer-title-row">
            <h1 class="mono explorer-title">{collection.name}</h1>
            <Badge tone={collection.write === 'direct' ? 'pink' : 'neutral'}>{collection.write}</Badge>
            {definition?.stream && <Badge>stream</Badge>}
            <Badge tone={health.ok ? 'ok' : 'pink'}>{health.label}</Badge>
          </div>
        </div>

        <div class="explorer-actions">
          <span class="explorer-updated">Updated {updated}</span>

          <Button href={pageHref(current, query)} class="icon-only" aria-label="Refresh">
            <Icon name="refresh" />
          </Button>

          <MenuButton menu="collection-actions">Actions</MenuButton>

          <Button variant="dark" href={pageHref(current, query, { tab: 'documents' })}>
            Explore documents
          </Button>
        </div>
      </header>

      <ActionsMenu current={current} collection={collection} definition={definition} query={query} />

      <nav class="explorer-tabs" aria-label="Collection">
        {TABS.map(item => (
          <a
            class={item.id === query.tab ? 'explorer-tab on' : 'explorer-tab'}
            href={pageHref(current, query, { tab: item.id })}
            aria-current={item.id === query.tab ? 'page' : undefined}
          >
            {item.label}
          </a>
        ))}
      </nav>
    </>
  );
};

const Target: FC<{ name: string; tab: ExplorerTab }> = ({ name, tab }) => (
  <>
    <input type="hidden" name="coll" value={name} />
    <input type="hidden" name="tab" value={tab} />
  </>
);

const ActionsMenu: FC<{ current: Current; collection: CollectionInfo; definition?: CollectionDefinition; query: ExplorerQuery }> = ({ current, collection, definition, query }) => {
  const name = collection.name;
  const direct = collection.write === 'direct';
  const logical = Boolean(definition?.partition_by);

  const compact = confirm({ title: `Compact ${name}?`, body: 'ndb seals the memtable and merges every segment into one. The page waits for the end.', action: 'Compact', tone: 'default' });
  const truncate = confirm({ title: `Truncate ${name}?`, body: 'This removes every document. The definition and the indexes stay.', action: 'Truncate' });
  const drop = confirm({ title: `Drop ${name}?`, body: 'This removes the documents, the definition and the positions of the watch consumers.', action: 'Drop' });

  return (
    <Menu id="collection-actions" label="Collection actions">
      {!logical && (
        <form method="post" action={actionUrl(current, 'compact')} {...compact}>
          <Target name={name} tab={query.tab} />
          <MenuItem description="Merge every segment into one">Compact</MenuItem>
        </form>
      )}

      <MenuItem
        href={listHref(current, query, { dialog: 'rename' })}
        description="Give the collection a new name"
        {...openDialog('rename-collection')}
      >
        Rename
      </MenuItem>

      {!direct && (
        <form method="post" action={actionUrl(current, 'truncate')} {...truncate}>
          <Target name={name} tab={query.tab} />
          <MenuItem tone="danger" description="Remove every document">
            Truncate
          </MenuItem>
        </form>
      )}

      {!direct && (
        <form method="post" action={actionUrl(current, 'drop')} {...drop}>
          <Target name={name} tab={query.tab} />
          <MenuItem tone="danger" description="Remove the collection">
            Drop
          </MenuItem>
        </form>
      )}
    </Menu>
  );
};

const RenameDialog: FC<{ current: Current; query: ExplorerQuery; name: string }> = ({ current, query, name }) => (
  <Dialog
    id="rename-collection"
    title={`Rename ${name}`}
    description="The documents, the indexes and the watch positions move with the name."
    open={query.dialog === 'rename'}
    closeHref={pageHref(current, query)}
  >
    <form class="explorer-form" method="post" action={actionUrl(current, 'rename')}>
      <Target name={name} tab={query.tab} />

      <Field id="rename-to" label="New name">
        <Input id="rename-to" name="to" mono required pattern="[a-z0-9][a-z0-9_\-]{0,63}" />
      </Field>

      <label class="check">
        <input type="checkbox" name="replace" value="1" />
        Replace a collection that has this name (it stays restorable for its PITR window)
      </label>

      <div class="form-actions">
        <Button variant="dark">Rename</Button>
      </div>
    </form>
  </Dialog>
);

const EditDefinitionDialog: FC<{ current: Current; query: ExplorerQuery; definition: CollectionDefinition }> = ({ current, query, definition }) => {
  const durabilities = [
    { id: 'strict', label: 'strict (sync the WAL before the answer)' },
    { id: 'relaxed', label: 'relaxed (200 ms window)' },
  ];

  const fromYaml = Boolean(definition.ops_from_yaml);
  const canExpire = !definition.stream && !definition.ttl && !definition.expires_field;

  return (
    <Dialog
      id="edit-definition"
      title="Edit definition"
      description="ndb merges these keys into the definition. A key that is set (time, ttl, expires_field, write, path) cannot change."
      open={query.dialog === 'edit'}
      closeHref={pageHref(current, query)}
    >
      <form class="explorer-form" method="post" action={actionUrl(current, 'definition')}>
        <Target name={definition.name} tab={query.tab} />

        {fromYaml && <p class="hint">ndb.yml sets durability, retention, wal_retention and pitr of this collection. Change them there.</p>}

        {!fromYaml && (
          <>
            <Field id="edit-durability" label="durability">
              <Select id="edit-durability" name="durability" options={durabilities} value={definition.durability ?? 'strict'} />
            </Field>

            <Field id="edit-retention" label="retention">
              <Input id="edit-retention" name="retention" mono value={definition.retention ?? ''} placeholder="180d" />
            </Field>

            <Field id="edit-wal-retention" label="wal_retention">
              <Input id="edit-wal-retention" name="wal_retention" mono value={definition.wal_retention ?? ''} placeholder="1d" />
            </Field>

            <Field id="edit-pitr" label="pitr (off, or a duration up to 7d)">
              <Input id="edit-pitr" name="pitr" mono value={definition.pitr ?? ''} placeholder="1d" />
            </Field>
          </>
        )}

        {!definition.time && (
          <Field id="edit-time" label="time (not set yet)">
            <Input id="edit-time" name="time" mono placeholder="created_at" />
          </Field>
        )}

        {canExpire && (
          <>
            <Field id="edit-ttl" label="ttl (not set yet; needs time)">
              <Input id="edit-ttl" name="ttl" mono placeholder="30d" />
            </Field>

            <Field id="edit-expires-field" label="expires_field (not set yet; not with ttl)">
              <Input id="edit-expires-field" name="expires_field" mono placeholder="expires_at" />
            </Field>
          </>
        )}

        <div class="form-actions">
          <Button variant="dark">Save definition</Button>
        </div>
      </form>
    </Dialog>
  );
};

function writeText(write: string): string {
  return write === 'direct' ? 'direct (apps append files)' : 'engine (SDK writes)';
}

function pitrText(pitr?: PitrInfo): string {
  if (!pitr) return '—';

  return pitr.window === 'off' ? 'Off' : `On · window ${pitr.window}`;
}

function diskSegments(collection: CollectionInfo): Segment[] {
  return [
    { label: 'segments', value: collection.bytes.segments, tone: 'ink' },
    { label: 'WAL', value: collection.bytes.wal, tone: 'pink' },
    { label: 'meta', value: collection.bytes.meta, tone: 'gray' },
  ];
}

const Mono: FC<{ value?: string }> = ({ value }) => <span class="mono explorer-mono">{value || '—'}</span>;

const OverviewTab: FC<DetailProps> = ({ current, collection, definition, pitr, query }) => {
  const average = collection.rows > 0 ? formatBytes(Math.round(collection.total / collection.rows)) : '—';
  const rows = indexRows(collection.indexes, definition?.unique).filter(row => row.kind !== 'built in');

  const general = [
    { label: 'Documents', value: formatCount(collection.rows) },
    { label: 'Size', value: formatBytes(collection.total) },
    { label: 'Average document', value: average },
    { label: 'Write mode', value: writeText(collection.write) },
    { label: 'Quarantined lines', value: formatCount(collection.quarantined) },
    { label: 'PITR', value: pitrText(pitr) },
  ];

  const settings = [
    { label: 'time', value: <Mono value={definition?.time} /> },
    { label: 'durability', value: <Mono value={definition?.durability} /> },
    { label: 'ttl', value: <Mono value={definition?.ttl} /> },
    { label: 'expires_field', value: <Mono value={definition?.expires_field} /> },
    { label: 'retention', value: <Mono value={definition?.retention} /> },
    { label: 'wal_retention', value: <Mono value={definition?.wal_retention} /> },
    ...(definition?.path ? [{ label: 'path', value: <Mono value={definition.path} /> }] : []),
    ...(definition?.pitr ? [{ label: 'pitr', value: <Mono value={definition.pitr} /> }] : []),
    ...(definition?.partition_by ? [{ label: 'partition_by', value: <Mono value={definition.partition_by} /> }] : []),
    ...(definition?.partition_of ? [{ label: 'partition_of', value: <Mono value={definition.partition_of} /> }] : []),
  ];

  const edit = (
    <Button size="sm" href={listHref(current, query, { dialog: 'edit' })} {...openDialog('edit-definition')}>
      Edit
    </Button>
  );

  const manage = (
    <a class="explorer-card-link" href={pageHref(current, query, { tab: 'indexes' })}>
      Manage
    </a>
  );

  return (
    <>
      <Card>
        <CardHeader title="General information" />
        <DescriptionList items={general} />
      </Card>

      <div class="explorer-pair">
        <Card class="explorer-definition">
          <CardHeader title="Definition" action={definition ? edit : undefined} />
          <DescriptionList items={settings} />
        </Card>

        <Card>
          <CardHeader title="Indexes and keys" action={manage} />

          {rows.length === 0 && <p class="hint">Only the _id index. A filter on another field scans the segments.</p>}

          <ul class="explorer-keys">
            {rows.map(row => (
              <KeyRow row={row} />
            ))}
          </ul>
        </Card>
      </div>

      <Card>
        <div class="explorer-storage-head">
          <h2 class="card-title">Storage</h2>
          <span class="hint">WAL or source files → memtable → Parquet segments</span>
        </div>

        <SegmentBar segments={diskSegments(collection)} label="Disk use by part" />

        <div class="explorer-storage-facts">
          <span>
            <strong>{formatCount(collection.segments)} segments</strong> <span class="explorer-fact-note">sealed · {formatBytes(collection.bytes.segments)}</span>
          </span>

          <span>
            <strong>{formatCount(collection.wal_files)} WAL files</strong> <span class="explorer-fact-note">not sealed yet · {formatBytes(collection.bytes.wal)}</span>
          </span>

          <span>
            <strong>meta</strong> <span class="explorer-fact-note">{formatBytes(collection.bytes.meta)}</span>
          </span>
        </div>
      </Card>
    </>
  );
};

function kindLabel(row: IndexRow): string {
  return row.unique && row.kind !== 'built in' ? `${row.kind} unique` : row.kind;
}

const KeyRow: FC<{ row: IndexRow }> = ({ row }) => (
  <li class="explorer-key">
    <code class="mono explorer-mono">{row.fields}</code>
    <Badge tone={row.kind === 'compound' ? 'pink' : 'neutral'}>{kindLabel(row)}</Badge>
    <span class={row.ready ? 'index-state ready' : 'index-state'}>{row.state}</span>
  </li>
);

const IndexesTab: FC<DetailProps> = ({ current, collection, definition, query, explain }) => {
  const name = collection.name;
  const rows = indexRows(collection.indexes, definition?.unique);
  const keys = rows.some(row => row.keyOnly);

  return (
    <>
      <div class="card flush">
        <div class="explorer-table-head">
          <h2 class="card-title">Indexes</h2>

          <p class="hint">
            The <code class="mono">_id</code> index always exists. Indexes are derived data: ndb can rebuild them from the NDJSON.
          </p>
        </div>

        <div class="index-table">
          <Table label="Indexes">
            <thead>
              <tr>
                <th>fields</th>
                <th>kind</th>
                <th>unique</th>
                <th>state</th>
                <th>serves</th>

                <th>
                  <span class="sr-only">actions</span>
                </th>
              </tr>
            </thead>

            <tbody>
              {rows.map(row => (
                <IndexLine current={current} name={name} row={row} />
              ))}
            </tbody>
          </Table>
        </div>

        {keys && (
          <p class="hint index-note">
            {NO_KEY_ROUTE} To remove one, make a new collection without the key, copy the documents to it, and rename it with replace.
          </p>
        )}
      </div>

      <div class="explorer-pair">
        <NewIndexForm current={current} name={name} />
        <ExplainForm current={current} collection={collection} query={query} explain={explain} />
      </div>
    </>
  );
};

const IndexLine: FC<{ current: Current; name: string; row: IndexRow }> = ({ current, name, row }) => {
  const also = row.unique ? ` The unique key ${row.fields} stays.` : '';

  const drop = confirm({
    title: `Drop the index ${row.fields}?`,
    body: `Queries that used it scan the collection again. You can make the index again: ndb builds it from the data.${also}`,
    action: 'Drop index',
  });

  return (
    <tr>
      <td class="mono strong index-fields">{row.fields}</td>

      <td>
        <Badge tone={row.kind === 'compound' ? 'pink' : 'neutral'}>{row.kind}</Badge>
      </td>

      {row.unique ? <td data-label="unique">yes</td> : <td class="index-none">—</td>}
      <td class={row.ready ? 'index-state ready' : 'index-state'}>{row.state}</td>
      <td class="index-serves">{row.serves}</td>

      <td class="index-actions">
        {row.droppable && (
          <form method="post" action={actionUrl(current, 'indexes/drop')} {...drop}>
            <Target name={name} tab="indexes" />
            <input type="hidden" name="index" value={row.fields} />

            <Button
              variant="ghost"
              size="sm"
              class="destructive"
              aria-label={`Drop the index ${row.fields}`}
            >
              Drop
            </Button>
          </form>
        )}

        {row.keyOnly && (
          <span class="index-fixed" title={NO_KEY_ROUTE}>
            no remove route
          </span>
        )}
      </td>
    </tr>
  );
};

const NewIndexForm: FC<{ current: Current; name: string }> = ({ current, name }) => (
  <Card>
    <CardHeader title="New index" />

    <form class="explorer-form" method="post" action={actionUrl(current, 'indexes')} data-index-form={name}>
      <Target name={name} tab="indexes" />

      <div class="form-field">
        <label class="label" for="index-fields">
          Fields, in order (1 to 4)
        </label>

        <div class="field-chips">
          <span class="field-chips-list" data-index-chips />

          <input
            id="index-fields"
            class="field-chips-input mono"
            type="text"
            name="fields"
            required
            placeholder="add a field, such as items.sku"
            {...noSpelling}
            autocomplete="off"
            data-index-input
          />
        </div>

        <p class="hint bad" data-index-error hidden />

        <p class="hint">With 2 or more fields it is one compound index. It serves a filter with equality on the first fields and a range or a sort on the next one.</p>
      </div>

      <label class="index-unique">
        <input type="checkbox" name="unique" value="1" />

        <span>
          <strong>Unique key</strong>
          <br />
          <span class="hint">One live document for each tuple of values. A second one gets 409 unique_violation. {NO_KEY_ROUTE}</span>
        </span>
      </label>

      <pre class="index-preview mono" data-index-preview>
        {requestText(indexRequest(name, ['{field}'], false))}
      </pre>

      <div class="form-actions">
        <Button variant="dark">Create index</Button>
      </div>
    </form>
  </Card>
);

const ExplainForm: FC<{ current: Current; collection: CollectionInfo; query: ExplorerQuery; explain?: ExplainResult }> = ({ current, collection, query, explain }) => {
  const result = explain?.result;
  const rows = formatCount(collection.rows);
  const indexed = result?.plan.startsWith('index:') ?? false;

  return (
    <Card class="explorer-explain">
      <CardHeader title="Which index serves a filter?" />

      <form class="explorer-form" method="get" action={`${base(current)}/explorer`}>
        <Target name={collection.name} tab="indexes" />

        <Field id="explain-filter" label="Filter">
          <Textarea id="explain-filter" name="filter" mono rows={3} required placeholder={'{"status":"paid"}'}>
            {query.filter ?? ''}
          </Textarea>
        </Field>

        <div class="form-actions">
          <Button>Explain</Button>
        </div>
      </form>

      {explain?.error && <p class="note bad">{explain.error}</p>}

      {result && (
        <dl class="explain-result">
          <div>
            <dt>plan</dt>
            <dd class="mono">{planText(result.plan)}</dd>
          </div>

          <div>
            <dt>examined</dt>
            <dd>{result.examined === undefined ? '—' : `${formatCount(result.examined)} of ${rows}`}</dd>
          </div>

          {indexed && (
            <div>
              <dt>without it</dt>
              <dd>scan of {rows}</dd>
            </div>
          )}
        </dl>
      )}

      <p class="hint">The plan and the examined count come from the X-Ndb-Plan and X-Ndb-Examined headers of a search with a limit of 1.</p>
    </Card>
  );
};

const StorageTab: FC<{ collection: CollectionInfo; pitr?: PitrInfo; quarantine?: QuarantineRow[] }> = ({ collection, pitr, quarantine = [] }) => {
  const parts = diskSegments(collection);

  const items = [
    { label: 'Segments', value: formatCount(collection.segments) },
    { label: 'WAL files', value: formatCount(collection.wal_files) },
    { label: 'Documents', value: formatCount(collection.rows) },
    { label: 'State', value: collection.asleep ? 'asleep (opens at the next call)' : 'open' },
    ...(pitr ? [{ label: 'PITR window on disk', value: formatBytes(pitr.bytes) }] : []),
  ];

  return (
    <>
      <Card>
        <div class="explorer-storage-head">
          <h2 class="card-title">Disk</h2>
          <span class="hint">total {formatBytes(collection.total)}</span>
        </div>

        <SegmentBar segments={parts} label="Disk use by part" />

        <ul class="disk-legend">
          {parts.map(part => (
            <li>
              <span class={`disk-swatch seg ${part.tone}`} aria-hidden="true" />
              <strong>{part.label}</strong>
              <span class="explorer-fact-note">{formatBytes(part.value)}</span>
            </li>
          ))}
        </ul>
      </Card>

      <Card>
        <CardHeader title="Files" />
        <DescriptionList items={items} />
        <p class="hint">Segments are sealed Parquet files. WAL files hold the writes that are not sealed yet. Use Actions, Compact to merge the segments.</p>
      </Card>

      {collection.write === 'direct' && <QuarantineCard total={collection.quarantined} rows={quarantine} />}
    </>
  );
};

const QuarantineCard: FC<{ total: number; rows: QuarantineRow[] }> = ({ total, rows }) => (
  <div class="card flush">
    <div class="explorer-table-head">
      <h2 class="card-title">Quarantine</h2>
      <p class="hint">{total === 0 ? 'No bad line in the source files.' : `${formatCount(total)} bad lines. The oldest ${rows.length} are below.`}</p>
    </div>

    {rows.length > 0 && (
      <Table minWidth={640} label="Quarantined lines">
        <thead>
          <tr>
            <th>time</th>
            <th>file</th>
            <th class="num">offset</th>
            <th>error</th>
            <th>line</th>
          </tr>
        </thead>

        <tbody>
          {rows.map(row => (
            <tr>
              <td class="mono">{row.ts}</td>
              <td class="mono">{row.file}</td>
              <td class="num">{row.offset}</td>
              <td>{row.error}</td>
              <td class="mono">{row.line}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    )}
  </div>
);

const LocksTab: FC<{ name: string }> = ({ name }) => (
  <>
    <Card>
      <CardHeader title="Locks" />

      <p class="explorer-text">
        ndb has no route that lists the open locks, so this page cannot show them. A lock gives one caller a document for a short time: the other writers wait. It ends by itself after its <code class="mono">ttl</code> (30 s by default, at most 5 minutes).
      </p>

      <p class="explorer-text">Every document also has a version, <code class="mono">_v</code>. A write with an old version changes nothing, so most code needs no lock.</p>
    </Card>

    <div class="explorer-pair">
      <Card>
        <CardHeader title="Take a lock" />
        <CodeBlock code={`POST /${name}/_lock/{id}\n{"ttl":"30s","wait":"10s"}`} />
        <p class="hint">The answer has the token, expires_at and the document. Send the token with each write of the holder: ?lock=&lt;token&gt;. When another caller holds the lock after wait, the answer is 423 locked.</p>
      </Card>

      <Card>
        <CardHeader title="Release a lock" />
        <CodeBlock code={`POST /${name}/_unlock/{id}\n{"token":"<token>"}`} />
        <p class="hint">A token that holds no lock gets 409 lock_expired. To lock a set of documents under one token, send ids to POST /{name}/_lock.</p>
      </Card>
    </div>
  </>
);

// draftError is the reason the restore form cannot go to its confirm step.
function draftError(draft: RestoreDraft, to: string): string | undefined {
  if (!draft.at || seconds(draft.at) === undefined) return 'Give the point in time as YYYY-MM-DD HH:MM:SS.';

  if (draft.where !== 'place' && !COLLECTION_NAME.test(to)) return 'The name of the copy has lower case letters, digits, _ and -, and starts with a letter or a digit.';

  return undefined;
}

const BackupsTab: FC<{ current: Current; name: string; pitr?: PitrInfo; draft?: RestoreDraft }> = ({ current, name, pitr, draft = {} }) => {
  const open = (
    <Button size="sm" href={`${base(current)}/backups`}>
      Open Backups
    </Button>
  );

  if (!pitr) {
    return (
      <Card>
        <Empty title="No PITR data" description="This collection has no restore window yet. A direct collection has none: its history is the files of the app." action={open} />
      </Card>
    );
  }

  const items = [
    { label: 'Window', value: pitr.window },
    { label: 'Oldest', value: <Mono value={pitr.oldest} /> },
    { label: 'Newest', value: <Mono value={pitr.newest} /> },
    { label: 'Snapshots', value: `${pitr.snapshots} · ${formatBytes(pitr.bytes)}` },
  ];

  const to = copyName(name, draft);
  const problem = draft.confirm ? draftError(draft, to) : undefined;
  const confirming = Boolean(draft.confirm && !problem);

  const formParams = new URLSearchParams({ coll: name, tab: 'backups', at: draft.at ?? '', where: draft.where ?? 'copy', to });
  const formHref = `${base(current)}/explorer?${formParams}#pitr-restore`;

  return (
    <>
      <Card>
        <CardHeader title="Point-in-time recovery" action={open} />
        <WindowBar item={pitr} />
        <DescriptionList items={items} />
        <p class="hint">The dark part of the bar can be restored. Oldest is the first instant that a restore reaches.</p>
      </Card>

      {pitr.oldest ? (
        <section class="card restore-card" id="pitr-restore" aria-label={`Restore ${name}`}>
          <div class="restore-head">
            <h2 class="card-title">
              Restore <span class="mono">{name}</span>
            </h2>
          </div>

          <p class="hint">
            {name} as it was at an instant. Restorable from {when(pitr.oldest)}. You confirm before ndb does it.
          </p>

          {problem && <p class="note bad">{problem}</p>}

          <form class="stack" method="get" action={`${base(current)}/explorer`}>
            <Target name={name} tab="backups" />
            <input type="hidden" name="confirm" value="1" />

            <RestoreFields
              item={pitr}
              draft={draft}
              placeHint={`${name} becomes as it was at that instant. The current ${name} goes to the trash, where it stays restorable for its window.`}
            />

            <div class="form-actions">
              <Button variant="dark" type="submit">
                Restore
              </Button>
            </div>
          </form>

          {confirming && (
            <RestoreConfirm
              collection={name}
              at={draft.at ?? ''}
              to={to}
              mode={draft.where === 'place' ? 'replace' : 'copy'}
              action={`${base(current)}/backups/restore`}
              cancelHref={formHref}
              back="explorer"
            />
          )}
        </section>
      ) : (
        <Card>
          <Empty title="Nothing to restore yet" description="The window starts at the first snapshot of this collection." />
        </Card>
      )}
    </>
  );
};
