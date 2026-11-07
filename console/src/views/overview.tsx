import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { formatBytes, formatCount, formatUptime, formatVersion } from '@/format';
import type { Definition, Size, Stats } from '@/ndb';
import { Badge } from '@/ui/badge';
import { Card, CardHeader } from '@/ui/card';
import { Empty } from '@/ui/empty';
import { type Segment, SegmentBar } from '@/ui/progress';
import { Stat } from '@/ui/stat';
import { Table } from '@/ui/table';

export interface OverviewPageProps {
  current: Current;
  stats: Stats;
  size?: Size;
  definitions: Definition[];
}

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

export const OverviewPage: FC<OverviewPageProps> = ({ current, stats, size, definitions }) => {
  const byName = new Map(definitions.map(definition => [definition.name, definition]));
  const quarantined = stats.totals.quarantined;

  return (
    <Layout title="Overview" current={current} tab="overview">
      <div class="grid-stats">
        <Stat label="Rows" value={formatCount(stats.totals.rows)} sub={`${stats.totals.collections} collections`} />
        <Stat label="Collections" value={String(stats.totals.collections)} sub={quarantined > 0 ? `${quarantined} quarantined lines` : 'no quarantined lines'} />
        <Stat label="Version" value={formatVersion(stats.version)} sub={`up ${formatUptime(stats.uptime_seconds)}`} />
        <Stat label="Disk" value={formatBytes(stats.totals.bytes)} sub={`${stats.totals.segments} segments · ${stats.totals.wal_files} WAL files`} />
      </div>

      <section class="section">
        <h2 class="section-title">Collections</h2>

        {stats.collections.length === 0 ? (
          <Card>
            <Empty title="No collections yet" description="Insert a document and the collection appears here." />
          </Card>
        ) : (
          <div class="card flush">
            <Table minWidth={760} label="Collections">
              <thead>
                <tr>
                  <th>name</th>
                  <th>write</th>
                  <th class="num">rows</th>
                  <th class="num">size</th>
                  <th>indexes</th>
                  <th>ttl</th>
                </tr>
              </thead>

              <tbody>
                {stats.collections.map(collection => (
                  <tr>
                    <td class="mono strong">{collection.name}</td>
                    <td>
                      <Badge tone={collection.write === 'direct' ? 'pink' : 'neutral'}>{collection.write}</Badge>
                    </td>
                    <td class="num">{collection.rows.toLocaleString('en-US')}</td>
                    <td class="num">{formatBytes(collection.total)}</td>
                    <td class="mono sub">{collection.indexes.length > 0 ? collection.indexes.map(index => index.field).join(', ') : '—'}</td>
                    <td class="sub">{ttlText(byName.get(collection.name))}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        )}
      </section>

      {size && <DiskCard size={size} />}
    </Layout>
  );
};

const DiskCard: FC<{ size: Size }> = ({ size }) => {
  const parts = sumParts(size);

  const segments: (Segment & { key: string })[] = [
    { key: 'segments', label: 'segments', value: parts.segments, tone: 'ink' },
    { key: 'wal', label: 'WAL', value: parts.wal, tone: 'pink' },
    { key: 'blobs', label: 'blobs', value: parts.blobs, tone: 'gray' },
    { key: 'meta', label: 'meta', value: parts.meta, tone: 'gray' },
    { key: 'cursors', label: 'cursors', value: parts.cursors, tone: 'gray' },
  ];

  return (
    <Card>
      <CardHeader title="Disk" description={size.data_dir} />

      <SegmentBar segments={segments} label="Disk use by part" />

      <div class="legend">
        {segments.map(segment => (
          <span>
            {segment.label} {formatBytes(segment.value)}
          </span>
        ))}
        <span class="sub">total {formatBytes(size.total)}</span>
      </div>
    </Card>
  );
};
