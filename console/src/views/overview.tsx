import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { formatBytes, formatCount, formatUptime, formatVersion } from '@/format';
import type { CollectionStats, Definition, Size, Stats } from '@/ndb';
import type { Metrics } from '@/operations';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { Empty } from '@/ui/empty';
import { Icon } from '@/ui/icon';
import { type Segment, SegmentBar } from '@/ui/progress';
import { Stat } from '@/ui/stat';
import { Table } from '@/ui/table';

export interface OverviewPageProps {
  current: Current;
  stats: Stats;
  size?: Size;
  definitions: Definition[];
  metrics?: Metrics;
  pitrWindow?: string;
  updated: string;
  tcpNoToken?: boolean;
}

type StreamDefinition = Definition & { stream?: boolean };
type SleepyStats = CollectionStats & { asleep?: boolean };

function sumParts(size: Size) {
  const parts = { segments: size.shared.segments, wal: size.shared.wal, meta: size.shared.meta, blobs: size.shared.blobs, cursors: size.shared.cursors };

  for (const collection of size.collections) {
    parts.segments += collection.bytes.segments;
    parts.wal += collection.bytes.wal;
    parts.meta += collection.bytes.meta;
    parts.blobs += collection.bytes.blobs;
    parts.cursors += collection.bytes.cursors;
  }

  return parts;
}

function ttlText(definition?: Definition): string {
  if (definition?.ttl) return definition.ttl;

  if (definition?.expires_field) return definition.expires_field;

  return '—';
}

function statusText(collection: SleepyStats): string {
  if (collection.asleep) return 'asleep';

  if (collection.indexes.some(index => index.state !== 'ready')) return 'building';

  return 'ready';
}

const plural = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`;

export const OverviewPage: FC<OverviewPageProps> = ({ current, stats, size, definitions, metrics, pitrWindow, updated, tcpNoToken = false }) => {
  const base = `c/${encodeURIComponent(current.name)}`;

  const byName = new Map<string, StreamDefinition>(definitions.map(definition => [definition.name, definition]));
  const streams = stats.collections.filter(collection => byName.get(collection.name)?.stream).length;
  const collections = stats.totals.collections - streams;

  const quarantined = stats.totals.quarantined;
  const memtable = metrics?.get('ndb_memtable_bytes');

  return (
    <Layout title="Dashboard" current={current} tab="overview">
      <header class="dash-head">
        <div class="dash-head-text">
          <h1 class="dash-title">Dashboard</h1>
          <p class="dash-lead">The state of this daemon: data, traffic and what needs your attention.</p>
        </div>

        <div class="dash-updated">
          <span>Updated {updated}</span>

          <Button
            href={base}
            class="icon-only"
            aria-label="Refresh"
            title="Refresh"
          >
            <Icon name="refresh" />
          </Button>
        </div>
      </header>

      <section class="chips" aria-label="Health">
        <Badge tone="ok" size="lg">Healthy</Badge>
        <Badge tone={quarantined > 0 ? 'warn' : 'neutral'} size="lg">{plural(quarantined, 'quarantined line')}</Badge>
        {tcpNoToken && <Badge tone="warn" size="lg">TCP has no token</Badge>}
      </section>

      <section class="grid-stats" aria-label="Totals">
        <Stat label="Documents" value={formatCount(stats.totals.rows)} sub={`${plural(collections, 'collection')} · ${plural(streams, 'stream')}`} />
        <Stat label="Disk" value={formatBytes(stats.totals.bytes)} sub="segments + WAL + meta" />
        {memtable !== undefined && <Stat label="Memory" value={formatBytes(memtable)} sub="memtables of all collections" />}
        <Stat label="Version" value={formatVersion(stats.version)} sub={`up ${formatUptime(stats.uptime_seconds)}`} />
      </section>

      <div class="dash-grid">
        {metrics && <TrafficCard metrics={metrics} />}
        {size && <DiskCard size={size} pitrWindow={pitrWindow} backups={`${base}/backups`} />}
      </div>

      <section class="card flush">
        <div class="dash-table-head">
          <h2 class="card-title">Collections and streams</h2>
          <a class="dash-link" href={`${base}/explorer`}>Open Collections</a>
        </div>

        {stats.collections.length === 0 ? (
          <div class="dash-table-empty">
            <Empty title="No collections yet" description="Insert a document and the collection appears here." />
          </div>
        ) : (
          <Table minWidth={760} label="Collections and streams">
            <thead>
              <tr>
                <th>name</th>
                <th>kind</th>
                <th class="num">documents</th>
                <th>indexes</th>
                <th>ttl</th>
                <th>status</th>
              </tr>
            </thead>

            <tbody>
              {stats.collections.map(collection => (
                <CollectionRow collection={collection} definition={byName.get(collection.name)} base={base} />
              ))}
            </tbody>
          </Table>
        )}
      </section>
    </Layout>
  );
};

const CollectionRow: FC<{ collection: SleepyStats; definition?: StreamDefinition; base: string }> = ({ collection, definition, base }) => {
  const stream = definition?.stream === true;
  const name = encodeURIComponent(collection.name);
  const href = stream ? `${base}/streams?stream=${name}` : `${base}/explorer?coll=${name}`;

  const kind = stream ? 'stream' : collection.write;
  const status = statusText(collection);
  const statusClass = status === 'ready' ? 'dash-status ok' : 'dash-status';

  return (
    <tr>
      <td class="mono strong">
        <a href={href}>{collection.name}</a>
      </td>
      <td>
        <Badge tone={kind === 'direct' ? 'pink' : 'neutral'}>{kind}</Badge>
      </td>
      <td class="num">{collection.rows.toLocaleString('en-US')}</td>
      <td class="mono sub">{collection.indexes.length > 0 ? collection.indexes.map(index => index.field).join(' · ') : '—'}</td>
      <td class="sub">{ttlText(definition)}</td>
      <td class={statusClass}>{status}</td>
    </tr>
  );
};

const Figure: FC<{ label: string; value?: number }> = ({ label, value }) => {
  if (value === undefined) return null;

  return (
    <div>
      <div class="figure-label">{label}</div>
      <div class="figure-value">{formatCount(value)}</div>
    </div>
  );
};

// TrafficCard shows the counters of /_metrics. They count from the start of
// the daemon: it keeps no history, so the page shows no rate and no chart.
const TrafficCard: FC<{ metrics: Metrics }> = ({ metrics }) => {
  const watches = metrics.get('ndb_watch_streams');
  const rows = metrics.get('ndb_rows_returned_total');
  const canceled = metrics.get('ndb_requests_canceled_total');

  return (
    <Card>
      <div class="dash-card-head">
        <h2 class="card-title">Traffic</h2>
        <span class="dash-card-note">from /_metrics, since the start</span>
      </div>

      <div class="figures">
        <Figure label="Requests" value={metrics.get('ndb_requests_total')} />
        <Figure label="Writes" value={metrics.get('ndb_writes_total')} />
        <Figure label="Errors" value={metrics.get('ndb_request_errors_total')} />
      </div>

      <div class="dash-foot">
        {watches !== undefined && <span>open watch streams {formatCount(watches)}</span>}
        {rows !== undefined && <span>rows returned {formatCount(rows)}</span>}
        {canceled !== undefined && <span>canceled requests {formatCount(canceled)}</span>}
      </div>
    </Card>
  );
};

const DiskCard: FC<{ size: Size; pitrWindow?: string; backups: string }> = ({ size, pitrWindow, backups }) => {
  const parts = sumParts(size);

  const segments: (Segment & { note: string })[] = [
    { label: 'segments', note: 'Parquet, sealed', value: parts.segments, tone: 'ink' },
    { label: 'WAL', note: 'NDJSON, the truth', value: parts.wal, tone: 'pink' },
    { label: 'indexes and meta', note: 'can be rebuilt', value: parts.meta, tone: 'gray' },
    { label: 'blobs and cursors', note: 'shared', value: parts.blobs + parts.cursors, tone: 'light' },
  ];

  return (
    <Card>
      <div class="dash-card-head">
        <h2 class="card-title">Disk</h2>
        <span class="dash-card-note mono">{size.data_dir}</span>
      </div>

      <SegmentBar segments={segments} label="Disk use by part" />

      <div class="disk-legend">
        {segments.map(segment => (
          <span>
            <strong>{segment.label}</strong> {formatBytes(segment.value)} <span class="disk-note">{segment.note}</span>
          </span>
        ))}
      </div>

      <a class="dash-link" href={backups}>
        {pitrWindow ? `PITR window: ${pitrWindow} · open Backups` : 'Open Backups'}
      </a>
    </Card>
  );
};
