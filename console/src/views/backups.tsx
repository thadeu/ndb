import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';

export interface BackupsPageProps {
  current: Current;
  online: boolean;
  error?: string;
}

const BACKUP_COMMANDS = ['ndb backup /backups/daily -gzip', 'ndb backup - | aws s3 cp - s3://bucket/daily.ndb'].join('\n');

const RESTORE_COMMANDS = ['systemctl stop ndb', 'sudo -u ndb ndb restore /backups/daily.ndb.gz -data-dir /var/lib/ndb', 'systemctl start ndb'].join('\n');

export const BackupsPage: FC<BackupsPageProps> = ({ current, online, error }) => {
  const action = `c/${encodeURIComponent(current.name)}/backups/download`;

  return (
    <Layout title="Backups" current={current} tab="backups">
      <header class="page-head">
        <div class="page-head-text">
          <h1>Backups</h1>
          <p class="lead">One NDJSON file with the catalog, the WAL, the watch positions, and the blobs. It is taken at a mark between two commits, while ndb runs.</p>
        </div>
      </header>

      {error && <p class="note bad">{error}</p>}

      <Card>
        <CardHeader title="Download a backup" description={current.url} />

        <form class="stack" method="post" action={action} data-backup-form>
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
        <CardHeader title="Restore" description="Not in the console: a restore swaps the data directory, so ndb must be stopped." />

        <CodeBlock code={RESTORE_COMMANDS} />

        <p class="hint">Restore refuses a data dir that is not empty unless you add <span class="mono">-force</span>. Documents keep their <span class="mono">_v</span>, and watch consumers resume after their last ack.</p>
      </Card>
    </Layout>
  );
};
