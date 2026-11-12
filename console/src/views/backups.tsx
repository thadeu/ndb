import type { FC } from 'hono/jsx';

import { RestoreConfirm, RestoreFields, WindowBar, copyName, type RestoreDraft } from '@/components/restore-form';
import { formatBytes } from '@/format';
import { Layout, type Current } from '@/html/layout';
import type { PitrInfo } from '@/ndb';
import { pageOf, seconds, when } from '@/pitr';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { Tabs, type TabItem } from '@/ui/tabs';

// BackupsQuery is the state of the page in the URL: the PITR page, and the
// dialog that is open (restore of one collection, or rename). With confirm,
// the restore card shows its confirm step. restored is the collection that
// the last restore made.
export interface BackupsQuery {
  page?: number;
  restore?: string;
  at?: string;
  where?: string;
  confirm?: boolean;
  restored?: string;
  rename?: boolean;
  from?: string;
  to?: string;
}

export interface BackupsPageProps {
  current: Current;
  online: boolean;
  pitr: { default: string; collections: PitrInfo[] };
  rows?: Record<string, number>;
  query?: BackupsQuery;
  notice?: string;
  error?: string;
}

const lines = (...rows: string[]) => rows.join('\n');

const code = (id: string, label: string, text: string): TabItem => ({ id, label, content: <CodeBlock code={text} /> });

// The same work from a terminal. The backup is taken while ndb runs. A
// restore of a backup file swaps the data dir, so the daemon must be stopped.
// PITR runs on the live daemon, so the SDKs can do it too.
const TERMINAL_TABS: TabItem[] = [
  code(
    'cli',
    'CLI',
    lines(
      '# backup: one file, while ndb runs',
      'ndb backup /backups/daily -gzip',
      'ndb backup - | aws s3 cp - s3://bucket/daily.ndb',
      '',
      '# restore a backup file: the daemon must be stopped',
      'ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb',
      '',
      '# point in time: on the live daemon',
      'ndb pitr',
      'ndb restore calls -at 15m -to calls_restored',
      'ndb rename calls_restored calls -replace -yes',
    ),
  ),
  code(
    'systemd',
    'systemd',
    lines(
      'sudo -u ndb ndb backup /backups/daily.ndb.gz -gzip',
      '# every night: a systemd timer or cron runs the same line',
      '',
      'systemctl stop ndb',
      'sudo -u ndb ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb',
      'systemctl start ndb',
    ),
  ),
  code(
    'docker',
    'Docker',
    lines(
      'docker exec ndb /usr/local/bin/ndb backup - > daily.ndb',
      '',
      'docker stop ndb',
      'docker run --rm -v ndb-data:/var/lib/ndb -v /backups:/backups:ro \\',
      '  ghcr.io/thadeu/ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb',
      'docker start ndb',
      '',
      'docker exec ndb /usr/local/bin/ndb restore calls -at 15m -to calls_restored',
      'docker exec ndb /usr/local/bin/ndb rename calls_restored calls -replace -yes',
    ),
  ),
  code(
    'voodu',
    'Voodu',
    lines(
      'vd exec ndb -- ndb backup - > daily.ndb',
      '',
      '# 1. replicas = 0 in ndb.voodu',
      'vd apply -f ndb.voodu -y',
      '# 2. on the node, on the path of the data volume',
      'ndb restore /backups/daily.ndb.gz -data-dir <data volume path>',
      '# 3. replicas = 1 again',
      'vd apply -f ndb.voodu -y',
      '',
      'vd exec ndb -- ndb restore calls -at 15m -to calls_restored',
      'vd exec ndb -- ndb rename calls_restored calls -replace -yes',
    ),
  ),
  code('curl', 'curl', lines("curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_restore -d '{\"at\":\"2026-11-12T14:04:00Z\",\"to\":\"calls_restored\"}'", "curl --unix-socket /run/ndb/ndb.sock http://ndb/calls_restored/_rename -d '{\"to\":\"calls\",\"replace\":true}'")),
  code('go', 'Go', lines('err := calls.Restore(ctx, at, "calls_restored")', 'err = restored.Rename(ctx, "calls", true)')),
  code('ruby', 'Ruby', lines('client.restore("calls", at: at, to: "calls_restored")', 'client.rename("calls_restored", "calls", replace: true)')),
  code('ts', 'TypeScript', lines("await calls.restore({ at, to: 'calls_restored' })", "await restored.rename('calls', { replace: true })")),
];

export function backupsHref(name: string, query: BackupsQuery = {}): string {
  const params = new URLSearchParams();

  if (query.page && query.page > 1) params.set('page', String(query.page));
  if (query.restore) params.set('restore', query.restore);
  if (query.rename) params.set('rename', '1');

  const search = params.toString();

  return `c/${encodeURIComponent(name)}/backups${search ? `?${search}` : ''}`;
}

export const BackupsPage: FC<BackupsPageProps> = ({ current, online, pitr, rows = {}, query = {}, notice, error }) => {
  const restoring = pitr.collections.find(item => item.collection === query.restore);
  const renaming = !restoring && Boolean(query.rename || query.from);

  return (
    <Layout title="Backups" current={current} tab="backups">
      <header class="page-head">
        <div class="page-head-text">
          <h1>Backups</h1>
          <p class="lead">Go back to a point in time, or take a full copy of the data directory.</p>
        </div>
      </header>

      {notice && (
        <p class="note ok">
          {notice} {query.restored && <a href={explorerHref(current, query.restored)}>Open {query.restored} in the Explorer</a>}
        </p>
      )}

      {error && <p class="note bad">{error}</p>}

      <PitrCard current={current} pitr={pitr} query={query} />

      <div class="backup-grid">
        {restoring && <RestoreCard current={current} item={restoring} rows={rows[restoring.collection]} query={query} />}

        {renaming && <RenameCard current={current} query={query} />}

        {restoring || renaming ? (
          <div class="backup-side">
            <FullBackupCard current={current} online={online} />

            <TerminalCard />
          </div>
        ) : (
          <>
            <FullBackupCard current={current} online={online} />

            <TerminalCard />
          </>
        )}
      </div>

      <ConfirmHost />
    </Layout>
  );
};

function explorerHref(current: Current, collection: string, tab = 'documents'): string {
  return `c/${encodeURIComponent(current.name)}/explorer?${new URLSearchParams({ coll: collection, tab })}`;
}

const PitrRow: FC<{ current: Current; item: PitrInfo; page: number; on: boolean }> = ({ current, item, page, on }) => {
  const snapshots = `${item.snapshots} ${item.snapshots === 1 ? 'snapshot' : 'snapshots'}`;

  return (
    <li class={on ? 'pitr-row on' : 'pitr-row'}>
      <span class="pitr-name mono">
        {item.collection} {item.dropped && <Badge tone="pink">dropped</Badge>}
      </span>

      <WindowBar item={item} />

      <span class="pitr-meta">
        {snapshots} · {formatBytes(item.bytes)}
      </span>

      <Button
        variant="secondary"
        size="sm"
        href={`${backupsHref(current.name, { page, restore: item.collection })}#pitr-restore`}
      >
        Restore
      </Button>
    </li>
  );
};

const PitrCard: FC<{ current: Current; pitr: BackupsPageProps['pitr']; query: BackupsQuery }> = ({ current, pitr, query }) => {
  const { rows, page, pages } = pageOf(pitr.collections, query.page ?? 1);
  const status = pitr.default === 'off' ? 'off by default (pitr: off in ndb.yml)' : `default window ${pitr.default} · the bar shows what each collection can restore`;

  return (
    <Card class="flush">
      <div class="pitr-head">
        <h2 class="card-title">Point-in-time recovery</h2>

        <span class="hint">{status}</span>

        <Button variant="secondary" size="sm" href={`${backupsHref(current.name, { page, rename: true })}#pitr-rename`}>
          Rename a collection
        </Button>
      </div>

      {pitr.collections.length === 0 ? (
        <div class="pitr-empty">
          <Empty title="Nothing to restore yet" description={pitr.default === 'off' ? 'Set pitr in ndb.yml to turn it on.' : 'Collections appear here after their first write.'} />
        </div>
      ) : (
        <ul class="pitr-list" aria-label="PITR windows">
          {rows.map(item => (
            <PitrRow
              current={current}
              item={item}
              page={page}
              on={item.collection === query.restore}
            />
          ))}
        </ul>
      )}

      {pages > 1 && (
        <div class="pitr-pages">
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
    </Card>
  );
};

const RestoreCard: FC<{ current: Current; item: PitrInfo; rows?: number; query: BackupsQuery }> = ({ current, item, rows, query }) => {
  const base = `c/${encodeURIComponent(current.name)}`;
  const closeHref = backupsHref(current.name, { page: query.page });

  const draft: RestoreDraft = { at: query.at, where: query.where === 'place' ? 'place' : 'copy', to: query.to };
  const to = copyName(item.collection, draft);
  const confirming = Boolean(query.confirm && draft.at && seconds(draft.at) !== undefined);

  const formParams = new URLSearchParams({ restore: item.collection, at: draft.at ?? '', where: draft.where ?? 'copy', to });
  const formHref = `${base}/backups?${formParams}#pitr-restore`;

  return (
    <section class="card restore-card" id="pitr-restore" aria-label={`Restore ${item.collection}`}>
      <div class="restore-head">
        <h2 class="card-title">
          Restore <span class="mono">{item.collection}</span>
        </h2>

        <Badge tone="pink">step 1 of 2</Badge>
      </div>

      <p class="hint">A copy of {item.collection} as it was at an instant, in a new collection. Restorable from {when(item.oldest)}.</p>

      <form class="stack" method="get" action={`${base}/backups`}>
        <input type="hidden" name="restore" value={item.collection} />
        <input type="hidden" name="confirm" value="1" />

        {query.page && query.page > 1 && <input type="hidden" name="page" value={String(query.page)} />}

        <RestoreFields
          item={item}
          draft={draft}
          placeHint={`The copy is made first. In step 2 you confirm the rename: the copy takes the name ${item.collection}, and the old one goes to the trash, where it stays restorable for its window.`}
        />

        {rows !== undefined && (
          <div class="restore-now">
            <span>
              <span class="mono">{item.collection}</span> has <strong>{rows.toLocaleString('en-US')}</strong> {rows === 1 ? 'document' : 'documents'} now.
            </span>

            <span class="grow" />

            <a href={explorerHref(current, item.collection)}>Open in Explorer</a>
          </div>
        )}

        <div class="form-actions">
          <Button variant="dark" type="submit">
            Restore a copy
          </Button>

          <Button variant="ghost" href={closeHref}>
            Cancel
          </Button>
        </div>
      </form>

      {confirming && (
        <RestoreConfirm
          collection={item.collection}
          at={draft.at ?? ''}
          to={to}
          mode={draft.where === 'place' ? 'steps' : 'copy'}
          action={`${base}/backups/restore`}
          cancelHref={formHref}
        />
      )}
    </section>
  );
};

const RenameCard: FC<{ current: Current; query: BackupsQuery }> = ({ current, query }) => {
  const closeHref = backupsHref(current.name, { page: query.page });
  const afterRestore = Boolean(query.from && query.to);

  const sure = confirm({
    title: 'Rename the collection?',
    body: 'With Replace on, the collection that has the new name now goes to the trash. It stays restorable for its PITR window, and its watch consumers start over. With Replace off, nothing is replaced.',
    action: afterRestore ? 'Put it in place' : 'Rename',
  });

  return (
    <section class="card restore-card" id="pitr-rename" aria-label="Rename a collection">
      <div class="restore-head">
        {afterRestore ? (
          <h2 class="card-title">
            Restore <span class="mono">{query.to}</span>
          </h2>
        ) : (
          <h2 class="card-title">Rename a collection</h2>
        )}

        {afterRestore && <Badge tone="pink">step 2 of 2</Badge>}
      </div>

      <p class="hint">Put a restored copy in place: its documents, indexes and watch positions move to the new name. With replace, the current collection goes to the trash, where it stays restorable for its window.</p>

      <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/backups/rename`} {...sure}>
        <Field id="rename-from" label="Collection">
          <Input
            id="rename-from"
            mono
            name="from"
            value={query.from ?? ''}
            placeholder="calls_restored"
            required
          />
        </Field>

        <Field id="rename-to" label="New name">
          <Input
            id="rename-to"
            mono
            name="to"
            value={query.to ?? ''}
            placeholder="calls"
            required
          />
        </Field>

        <label class="check">
          <input type="checkbox" name="replace" value="1" checked={Boolean(query.to)} />
          <span>Replace the collection with that name</span>
        </label>

        <div class="form-actions">
          <Button variant="dark" type="submit">
            {afterRestore ? 'Put it in place' : 'Rename'}
          </Button>

          <Button variant="ghost" href={closeHref}>
            {afterRestore ? 'Keep the copy only' : 'Cancel'}
          </Button>
        </div>
      </form>
    </section>
  );
};

const FullBackupCard: FC<{ current: Current; online: boolean }> = ({ current, online }) => (
  <Card>
    <h2 class="card-title">Full backup</h2>

    <p class="hint">One NDJSON file with the catalog, the WAL, the watch positions and the blobs, taken while ndb runs. ndb gives it only over the unix socket, so a TCP connection must use the CLI.</p>

    <form class="stack" method="post" action={`c/${encodeURIComponent(current.name)}/backups/download`} data-backup-form>
      <div class="form-actions">
        <Button variant="secondary" type="submit" disabled={!online}>
          Download backup
        </Button>

        <label class="check">
          <input type="checkbox" name="gzip" value="1" checked />
          <span>gzip</span>
        </label>

        <label class="check">
          <input type="checkbox" name="no_blobs" value="1" />
          <span>Leave out the blobs</span>
        </label>
      </div>

      <span class="hint" data-backup-status hidden />
    </form>

    {!online && <p class="note warn">This connection is TCP: use the command below.</p>}
  </Card>
);

const TerminalCard: FC = () => (
  <Card class="soft">
    <h2 class="card-title">From a terminal</h2>

    <Tabs id="terminal-how" label="From a terminal" items={TERMINAL_TABS} />

    <p class="hint">
      A restore of a backup file needs a stopped daemon on that data directory. It refuses a data dir that is not empty unless you add <span class="mono">-force</span>. The other flags are in the docs page Operations.
    </p>
  </Card>
);
