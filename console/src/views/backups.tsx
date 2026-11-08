import type { FC } from 'hono/jsx';

import { formatBytes } from '@/format';
import { Layout, type Current } from '@/html/layout';
import type { PitrInfo } from '@/ndb';
import { defaultAt, pageOf, when } from '@/pitr';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { DatePicker } from '@/ui/date-picker';
import { Dialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { Table } from '@/ui/table';
import { Tabs, type TabItem } from '@/ui/tabs';

// BackupsQuery is the state of the page in the URL: the PITR page, and the
// dialog that is open (restore of one collection, or rename).
export interface BackupsQuery {
  page?: number;
  restore?: string;
  at?: string;
  rename?: boolean;
  from?: string;
  to?: string;
}

export interface BackupsPageProps {
  current: Current;
  online: boolean;
  pitr: { default: string; collections: PitrInfo[] };
  query?: BackupsQuery;
  notice?: string;
  error?: string;
}

const lines = (...rows: string[]) => rows.join('\n');

const code = (id: string, label: string, text: string): TabItem => ({ id, label, content: <CodeBlock code={text} /> });

// The backup file: taken while ndb runs, from the host.
const BACKUP_TABS: TabItem[] = [
  code('systemd', 'systemd', lines('sudo -u ndb ndb backup /backups/daily.ndb.gz -gzip', '# every night: a systemd timer or cron runs the same line')),
  code('cli', 'CLI', lines('ndb backup /backups/daily -gzip', 'ndb backup - | aws s3 cp - s3://bucket/daily.ndb')),
  code('docker', 'Docker', lines('docker exec ndb /usr/local/bin/ndb backup - > daily.ndb', '# or a second container with the volumes:', 'docker run --rm -v ndb-run:/run/ndb -v /backups:/backups ghcr.io/thadeu/ndb backup /backups/daily -gzip')),
  code('voodu', 'Voodu', lines('vd exec ndb -- ndb backup - > daily.ndb', '# add -r <remote> as in your other vd commands')),
];

// Restoring a backup file swaps the data dir: the daemon must be stopped.
const RESTORE_TABS: TabItem[] = [
  code('systemd', 'systemd', lines('systemctl stop ndb', 'sudo -u ndb ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb', 'systemctl start ndb')),
  code('cli', 'CLI', lines('# the daemon must be stopped', 'ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb', 'aws s3 cp s3://bucket/daily.ndb - | ndb restore - -data-dir /var/lib/ndb')),
  code('docker', 'Docker', lines('docker stop ndb', 'docker run --rm -v ndb-data:/var/lib/ndb -v /backups:/backups:ro \\', '  ghcr.io/thadeu/ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb', 'docker start ndb')),
  code('voodu', 'Voodu', lines('# 1. replicas = 0 in ndb.voodu', 'vd apply -f ndb.voodu -y', '# 2. on the node, on the path of the data volume', 'ndb restore /backups/daily.ndb.gz -data-dir <data volume path>', '# 3. replicas = 1 again', 'vd apply -f ndb.voodu -y')),
];

// PITR runs on the live daemon, so the SDKs can do it too.
const PITR_TABS: TabItem[] = [
  code('cli', 'CLI', lines('ndb pitr', 'ndb restore calls -at 15m -to calls_restored', 'ndb rename calls_restored calls -replace -yes')),
  code('docker', 'Docker', lines('docker exec ndb /usr/local/bin/ndb restore calls -at 15m -to calls_restored', 'docker exec ndb /usr/local/bin/ndb rename calls_restored calls -replace -yes')),
  code('voodu', 'Voodu', lines('vd exec ndb -- ndb restore calls -at 15m -to calls_restored', 'vd exec ndb -- ndb rename calls_restored calls -replace -yes')),
  code('curl', 'curl', lines("curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_restore -d '{\"at\":\"2026-11-12T14:04:00Z\",\"to\":\"calls_restored\"}'", "curl --unix-socket /run/ndb/ndb.sock http://ndb/calls_restored/_rename -d '{\"to\":\"calls\",\"replace\":true}'")),
  code('go', 'Go', lines('err := calls.Restore(ctx, at, "calls_restored")', 'err = restored.Rename(ctx, "calls", true)')),
  code('ruby', 'Ruby', lines('client.restore("calls", at: at, to: "calls_restored")', 'client.rename("calls_restored", "calls", replace: true)')),
  code('ts', 'TypeScript', lines("await calls.restore({ at, to: 'calls_restored' })", "await restored.rename('calls', { replace: true })")),
];

const PITR_PRESETS = [
  { label: '5 minutes ago', ago: '5m' },
  { label: '15 minutes ago', ago: '15m' },
  { label: '1 hour ago', ago: '1h' },
  { label: '6 hours ago', ago: '6h' },
  { label: 'Start of the window', at: 'min' as const },
];

export function backupsHref(name: string, query: BackupsQuery = {}): string {
  const params = new URLSearchParams();

  if (query.page && query.page > 1) params.set('page', String(query.page));
  if (query.restore) params.set('restore', query.restore);
  if (query.rename) params.set('rename', '1');

  const search = params.toString();

  return `c/${encodeURIComponent(name)}/backups${search ? `?${search}` : ''}`;
}

export const BackupsPage: FC<BackupsPageProps> = ({ current, online, pitr, query = {}, notice, error }) => {
  const action = `c/${encodeURIComponent(current.name)}/backups`;

  return (
    <Layout title="Backups" current={current} tab="backups">
      <header class="page-head">
        <div class="page-head-text">
          <h1>Backups</h1>
          <p class="lead">Point in time recovery for the mistakes of the day, and backup files for everything else.</p>
        </div>
      </header>

      {notice && <p class="note ok">{notice}</p>}

      {error && <p class="note bad">{error}</p>}

      <PitrCard current={current} pitr={pitr} query={query} />

      <Card>
        <CardHeader title="Backup file" description="One NDJSON file with the catalog, the WAL, the watch positions, and the blobs, taken at a mark between two commits while ndb runs." />

        <form class="stack" method="post" action={`${action}/download`} data-backup-form>
          <label class="check">
            <input type="checkbox" name="gzip" value="1" />
            <span>Compress with gzip (.ndb.gz)</span>
          </label>

          <label class="check">
            <input type="checkbox" name="no_blobs" value="1" />
            <span>Leave out the blobs</span>
          </label>

          <div class="form-actions">
            <Button variant="primary" type="submit" disabled={!online}>
              Download backup
            </Button>

            <span class="hint" data-backup-status hidden />

            {!online && <span class="hint">ndb refuses a backup over TCP. Run <span class="mono">ndb backup</span> on the host, over the unix socket.</span>}
          </div>
        </form>
      </Card>

      <Card>
        <CardHeader title="Backup from the host" description="The CLI writes the file by itself, with the daemon running or stopped." />

        <Tabs id="backup-how" label="Backup from the host" items={BACKUP_TABS} />
      </Card>

      <Card>
        <CardHeader title="Restore a backup file" description="Not in the console: a restore swaps the data directory, so ndb must be stopped. There is no SDK call for it, since no daemon runs." />

        <Tabs id="restore-how" label="Restore a backup file" items={RESTORE_TABS} />

        <p class="hint">Restore refuses a data dir that is not empty unless you add <span class="mono">-force</span>. Documents keep their <span class="mono">_v</span>, and watch consumers resume after their last ack.</p>
      </Card>

      <RestoreDialog current={current} pitr={pitr} query={query} />

      <RenameDialog current={current} query={query} />
    </Layout>
  );
};

const PitrCard: FC<{ current: Current; pitr: BackupsPageProps['pitr']; query: BackupsQuery }> = ({ current, pitr, query }) => {
  const { rows, page, pages } = pageOf(pitr.collections, query.page ?? 1);
  const status = pitr.default === 'off' ? 'Off by default (pitr: off in ndb.yml).' : `On: every engine collection can go back ${pitr.default} (pitr in ndb.yml).`;

  const rename = (
    <Button variant="secondary" size="sm" href={backupsHref(current.name, { page, rename: true })}>
      Rename a collection
    </Button>
  );

  return (
    <Card>
      <CardHeader title="Point in time recovery (PITR)" description={`${status} A restore makes a copy in a new collection; a rename puts it in place.`} action={rename} />

      {pitr.collections.length === 0 ? (
        <Empty title="Nothing to restore yet" description={pitr.default === 'off' ? 'Set pitr in ndb.yml to turn it on.' : 'Collections appear here after their first write.'} />
      ) : (
        <Table minWidth={680} label="PITR windows">
          <thead>
            <tr>
              <th>collection</th>
              <th>window</th>
              <th>restorable from</th>
              <th class="num">snapshots</th>
              <th class="num">disk</th>
              <th />
            </tr>
          </thead>

          <tbody>
            {rows.map(item => (
              <tr>
                <td class="mono strong">
                  {item.collection} {item.dropped && <Badge tone="pink">dropped</Badge>}
                </td>
                <td class="mono">{item.window}</td>
                <td class="mono sub">{when(item.oldest)}</td>
                <td class="num">{item.snapshots}</td>
                <td class="num">{formatBytes(item.bytes)}</td>
                <td class="num">
                  <Button variant="ghost" size="sm" href={backupsHref(current.name, { page, restore: item.collection })}>
                    Restore
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {pages > 1 && (
        <div class="form-actions">
          {page > 1 && (
            <Button variant="ghost" size="sm" href={backupsHref(current.name, { page: page - 1 })}>
              Previous
            </Button>
          )}

          <span class="hint">
            Page {page} of {pages} · {pitr.collections.length} collections
          </span>

          {page < pages && (
            <Button variant="ghost" size="sm" href={backupsHref(current.name, { page: page + 1 })}>
              Next
            </Button>
          )}
        </div>
      )}

      <details class="stack">
        <summary class="hint">The same from the host or the SDKs</summary>

        <Tabs id="pitr-how" label="PITR from the host" items={PITR_TABS} />
      </details>
    </Card>
  );
};

const RestoreDialog: FC<{ current: Current; pitr: BackupsPageProps['pitr']; query: BackupsQuery }> = ({ current, pitr, query }) => {
  const item = pitr.collections.find(c => c.collection === query.restore);
  const closeHref = backupsHref(current.name, { page: query.page });

  if (!item) return null;

  return (
    <Dialog id="pitr-restore" title={`Restore ${item.collection}`} description={`A copy of ${item.collection} as it was at an instant, in a new collection. Restorable from ${when(item.oldest)}.`} open closeHref={closeHref}>
      <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/backups/restore`}>
        <input type="hidden" name="collection" value={item.collection} />

        <Field id="pitr-at" label="At (UTC)">
          <DatePicker id="pitr-at" name="at" label="At (UTC)" value={query.at ?? defaultAt(item.oldest)} min={item.oldest} max="now" presets={PITR_PRESETS} required />
        </Field>

        <Field id="pitr-to" label="To (a new collection)">
          <Input id="pitr-to" mono name="to" value={`${item.collection}_restored`} required />
        </Field>

        <div class="form-actions">
          <Button variant="primary" type="submit">
            Restore copy
          </Button>

          <Button variant="ghost" href={closeHref} data-dialog-close>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
};

const RenameDialog: FC<{ current: Current; query: BackupsQuery }> = ({ current, query }) => {
  const closeHref = backupsHref(current.name, { page: query.page });
  const open = Boolean(query.rename || query.from);

  if (!open) return null;

  return (
    <Dialog id="pitr-rename" title="Rename a collection" description="Put a restored copy in place: its documents, indexes and watch positions move to the new name. With replace, the current collection goes to the trash, where it stays restorable for its window." open closeHref={closeHref}>
      <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/backups/rename`}>
        <Field id="rename-from" label="Collection">
          <Input id="rename-from" mono name="from" value={query.from ?? ''} placeholder="calls_restored" required />
        </Field>

        <Field id="rename-to" label="New name">
          <Input id="rename-to" mono name="to" value={query.to ?? ''} placeholder="calls" required />
        </Field>

        <label class="check">
          <input type="checkbox" name="replace" value="1" checked={Boolean(query.to)} />
          <span>Replace the collection with that name</span>
        </label>

        <div class="form-actions">
          <Button variant="dark" type="submit">
            Rename
          </Button>

          <Button variant="ghost" href={closeHref} data-dialog-close>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
};
