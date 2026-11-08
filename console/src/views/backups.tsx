import type { FC } from 'hono/jsx';

import { formatBytes } from '@/format';
import { Layout, type Current } from '@/html/layout';
import type { PitrInfo } from '@/ndb';
import { defaultAt, pageOf, when } from '@/pitr';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { Dialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { Table } from '@/ui/table';

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

const BACKUP_COMMANDS = ['ndb backup /backups/daily -gzip', 'ndb backup - | aws s3 cp - s3://bucket/daily.ndb'].join('\n');

const RESTORE_COMMANDS = ['systemctl stop ndb', 'sudo -u ndb ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb', 'systemctl start ndb'].join('\n');

const PITR_COMMANDS = ['ndb pitr', 'ndb restore calls -at 15m -to calls_restored', 'ndb rename calls_restored calls -replace -yes'].join('\n');

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

        <CodeBlock code={BACKUP_COMMANDS} />
      </Card>

      <Card>
        <CardHeader title="Restore a backup file" description="Not in the console: a restore swaps the data directory, so ndb must be stopped." />

        <CodeBlock code={RESTORE_COMMANDS} />

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
        <summary class="hint">The same from the host</summary>

        <CodeBlock code={PITR_COMMANDS} />
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
          <Input id="pitr-at" mono type="datetime-local" step="1" name="at" value={query.at ?? defaultAt(item.oldest)} required />
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
