import type { FC } from 'hono/jsx';

import { Documents, type DocumentsProps } from '@/components/documents';
import { Layout, type Current } from '@/html/layout';
import { formatBytes, formatCount } from '@/format';
import type { CollectionStats, Definition, IndexState, PitrInfo, Stats } from '@/ndb';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { DescriptionList } from '@/ui/description-list';
import { Empty } from '@/ui/empty';
import { Icon } from '@/ui/icon';
import { type Segment, SegmentBar } from '@/ui/progress';
import { Table } from '@/ui/table';

export type ExplorerTab = 'overview' | 'documents' | 'indexes' | 'storage' | 'backups';

export interface ExplorerQuery {
  coll?: string;
  tab: ExplorerTab;
  find?: string;
}

export interface ExplorerPageProps {
  current: Current;
  stats: Stats;
  query: ExplorerQuery;
  definition?: Definition;
  pitr?: PitrInfo;
  documents?: Omit<DocumentsProps, 'current'>;
  updated: string;
}

const TABS: { id: ExplorerTab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'documents', label: 'Documents' },
  { id: 'indexes', label: 'Indexes' },
  { id: 'storage', label: 'Storage' },
  { id: 'backups', label: 'Backups' },
];

const NAV = [
  { label: 'Dashboard', path: '' },
  { label: 'Collections', path: '/explorer', on: true },
  { label: 'Dev Tools', path: '/devtools' },
  { label: 'Watch', path: '/watch' },
  { label: 'Backups', path: '/backups' },
];

function base(current: Current): string {
  return `c/${encodeURIComponent(current.name)}`;
}

function explorerHref(current: Current, coll: string, tab: ExplorerTab = 'overview', find?: string): string {
  const params = new URLSearchParams({ coll, tab });

  if (find) params.set('find', find);

  return `${base(current)}/explorer?${params}`;
}

export const ExplorerPage: FC<ExplorerPageProps> = props => {
  const { current, stats, query } = props;

  const find = (query.find ?? '').toLowerCase();
  const listed = stats.collections.filter(collection => collection.name.toLowerCase().includes(find));
  const selected = stats.collections.find(collection => collection.name === query.coll);
  const scripts = props.documents ? ['document-editor'] : [];

  return (
    <Layout title="Explorer" current={current} tab="explorer" scripts={scripts} bleed>
      <div class={selected ? "explorer has-coll" : "explorer"}>
        <SideNav current={current} />

        <CollectionList current={current} listed={listed} total={stats.collections.length} query={query} />

        <section class="explorer-main">
          {selected ? <Detail {...props} collection={selected} /> : <Empty title="Select a collection" description="Pick a collection in the list to see its details." />}
        </section>
      </div>
    </Layout>
  );
};

const SideNav: FC<{ current: Current }> = ({ current }) => (
  <nav class="explorer-nav" aria-label="Console">
    <p class="explorer-nav-title">ndb</p>

    {NAV.map(item => (
      <a class={item.on ? 'explorer-nav-link on' : 'explorer-nav-link'} href={`${base(current)}${item.path}`}>
        {item.label}
      </a>
    ))}
  </nav>
);

const CollectionList: FC<{ current: Current; listed: CollectionStats[]; total: number; query: ExplorerQuery }> = ({ current, listed, total, query }) => (
  <aside class="explorer-list" aria-label="Collections">
    <h2 class="explorer-list-title">
      Collections <span class="sub">({total})</span>
    </h2>

    <form class="explorer-find" method="get" action={`${base(current)}/explorer`}>
      {query.coll && <input type="hidden" name="coll" value={query.coll} />}
      <input type="hidden" name="tab" value={query.tab} />
      <input class="input" type="search" name="find" value={query.find ?? ''} placeholder="Find collections" aria-label="Find collections" />
    </form>

    <ul class="explorer-items">
      {listed.map(collection => (
        <CollectionItem current={current} collection={collection} query={query} />
      ))}
    </ul>

    {listed.length === 0 && <p class="sub">No collection matches.</p>}
  </aside>
);

const CollectionItem: FC<{ current: Current; collection: CollectionStats; query: ExplorerQuery }> = ({ current, collection, query }) => {
  const on = collection.name === query.coll;
  const className = on ? 'explorer-item on' : 'explorer-item';

  return (
    <li>
      <a class={className} href={explorerHref(current, collection.name, query.tab, query.find)} aria-current={on ? 'page' : undefined}>
        <span class="mono explorer-item-name">{collection.name}</span>
        <span class="sub">{formatCount(collection.rows)}</span>
      </a>
    </li>
  );
};

const Detail: FC<ExplorerPageProps & { collection: CollectionStats }> = props => {
  const { current, collection, query, updated } = props;

  return (
    <>
      <header class="explorer-head">
        <div>
          <a class="explorer-back sub" href={`${base(current)}/explorer`}>
            <Icon name="arrow-left" size={16} />
            Collections
          </a>
          <h1 class="mono explorer-title">{collection.name}</h1>
        </div>

        <div class="explorer-actions">
          <span class="sub explorer-updated">Last updated {updated}</span>

          <Button size="sm" href={explorerHref(current, collection.name, query.tab)} class="icon-only" aria-label="Refresh">
            <Icon name="refresh" />
          </Button>

          <Button variant="dark" size="sm" href={explorerHref(current, collection.name, 'documents')}>
            <Icon name="documents" />
            Explore documents
          </Button>
        </div>
      </header>

      <nav class="tabs explorer-tabs" aria-label="Collection">
        {TABS.map(item => (
          <a class={item.id === query.tab ? 'tab on' : 'tab'} href={explorerHref(current, collection.name, item.id, query.find)} aria-current={item.id === query.tab ? 'page' : undefined}>
            {item.label}
          </a>
        ))}
      </nav>

      {query.tab === 'overview' && <OverviewTab {...props} />}
      {query.tab === 'documents' && props.documents && <Documents current={current} {...props.documents} />}
      {query.tab === 'indexes' && <IndexesTab indexes={collection.indexes} definition={props.definition} />}
      {query.tab === 'storage' && <StorageTab collection={collection} />}
      {query.tab === 'backups' && <BackupsTab pitr={props.pitr} />}
    </>
  );
};

const Status: FC<{ ok: boolean; on: string; off: string }> = ({ ok, on, off }) => <Badge tone={ok ? 'ok' : 'neutral'}>{ok ? `✓ ${on}` : off}</Badge>;

const OverviewTab: FC<ExplorerPageProps & { collection: CollectionStats }> = ({ collection, definition, pitr }) => {
  const average = collection.rows > 0 ? formatBytes(Math.round(collection.total / collection.rows)) : '—';
  const ready = collection.indexes.every(index => index.state === 'ready');

  const general = [
    { label: 'Write mode', value: <Badge tone={collection.write === 'direct' ? 'pink' : 'neutral'}>{collection.write}</Badge> },
    { label: 'Status', value: <Status ok={collection.quarantined === 0} on="Healthy" off={`${collection.quarantined} quarantined`} /> },
    { label: 'Documents', value: formatCount(collection.rows) },
    { label: 'Size', value: formatBytes(collection.total) },
    { label: 'Average document', value: average },
    { label: 'Indexes', value: <Status ok={ready} on={`${collection.indexes.length} ready`} off="Building" /> },
    { label: 'PITR', value: <Status ok={Boolean(pitr?.newest)} on={`On · ${pitr?.window ?? ''}`} off="Not active" /> },
    { label: 'Time field', value: <span class="mono">{definition?.time ?? '—'}</span> },
  ];

  const settings = [
    { label: 'Durability', value: definition?.durability ?? 'strict' },
    { label: 'TTL', value: definition?.ttl ?? definition?.expires_field ?? '—' },
    { label: 'Retention', value: definition?.retention ?? '—' },
    { label: 'WAL retention', value: definition?.wal_retention ?? '—' },
  ];

  return (
    <>
      <Card>
        <CardHeader title="General information" />
        <DescriptionList items={general} />
      </Card>

      <Card>
        <CardHeader title="Settings" description="From the collection definition in the catalog." />
        <DescriptionList items={settings} />
      </Card>
    </>
  );
};

const IndexesTab: FC<{ indexes: IndexState[]; definition?: Definition }> = ({ indexes, definition }) => {
  const unique = new Set(definition?.unique ?? []);

  if (indexes.length === 0) {
    return (
      <Card>
        <Empty title="No secondary indexes" description="Only the _id index. Queries on other fields scan the segments." />
      </Card>
    );
  }

  return (
    <div class="card flush">
      <Table minWidth={480} label="Indexes">
        <thead>
          <tr>
            <th>field</th>
            <th>state</th>
            <th>unique</th>
            <th class="num">progress</th>
          </tr>
        </thead>

        <tbody>
          {indexes.map(index => (
            <tr>
              <td class="mono strong">{index.field}</td>
              <td>
                <Badge tone={index.state === 'ready' ? 'ok' : 'neutral'}>{index.state}</Badge>
              </td>
              <td>{unique.has(index.field) ? 'yes' : '—'}</td>
              <td class="num">{index.total > 0 ? `${index.done}/${index.total}` : '—'}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
};

const StorageTab: FC<{ collection: CollectionStats }> = ({ collection }) => {
  const segments: (Segment & { key: string })[] = [
    { key: 'segments', label: 'segments', value: collection.bytes.segments, tone: 'ink' },
    { key: 'wal', label: 'WAL', value: collection.bytes.wal, tone: 'pink' },
    { key: 'meta', label: 'meta', value: collection.bytes.meta, tone: 'gray' },
  ];

  const items = [
    { label: 'Segments', value: String(collection.segments) },
    { label: 'WAL files', value: String(collection.wal_files) },
    { label: 'Segment bytes', value: formatBytes(collection.bytes.segments) },
    { label: 'WAL bytes', value: formatBytes(collection.bytes.wal) },
  ];

  return (
    <Card>
      <CardHeader title="Disk" description={`total ${formatBytes(collection.total)}`} />
      <SegmentBar segments={segments} label="Disk use by part" />
      <DescriptionList items={items} />
    </Card>
  );
};

const BackupsTab: FC<{ pitr?: PitrInfo }> = ({ pitr }) => {
  if (!pitr) {
    return (
      <Card>
        <Empty title="No PITR data" description="This collection has no restore window yet." />
      </Card>
    );
  }

  const items = [
    { label: 'Window', value: pitr.window },
    { label: 'Oldest', value: <span class="mono">{pitr.oldest ?? '—'}</span> },
    { label: 'Newest', value: <span class="mono">{pitr.newest ?? '—'}</span> },
    { label: 'Snapshots', value: `${pitr.snapshots} · ${formatBytes(pitr.bytes)}` },
  ];

  return (
    <Card>
      <CardHeader title="Point-in-time recovery" />
      <DescriptionList items={items} />
    </Card>
  );
};
