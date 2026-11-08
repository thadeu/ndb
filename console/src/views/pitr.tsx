import type { FC } from 'hono/jsx';

import { formatBytes } from '@/format';
import { Layout, type Current } from '@/html/layout';
import type { PitrInfo } from '@/ndb';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { Table } from '@/ui/table';

export interface PitrPageProps {
  current: Current;
  window: string;
  collections: PitrInfo[];
  selected?: string;
  until: string;
  renameFrom?: string;
  renameTo?: string;
  notice?: string;
  error?: string;
}

const CLI = ['ndb pitr', 'ndb restore calls -until 15m -into calls_restored', 'ndb rename calls_restored calls -replace -yes'].join('\n');

// untilValue is the value of a datetime-local input, in UTC, to the second.
export function untilValue(date: Date): string {
  return date.toISOString().slice(0, 19);
}

function when(value?: string): string {
  return value ? `${value.slice(0, 19).replace('T', ' ')} UTC` : '—';
}

export const PitrPage: FC<PitrPageProps> = ({ current, window, collections, selected, until, renameFrom, renameTo, notice, error }) => {
  const base = `c/${encodeURIComponent(current.name)}/pitr`;
  const source = selected ?? collections.find(item => !item.dropped)?.collection ?? collections[0]?.collection ?? '';

  return (
    <Layout title="PITR" current={current} tab="pitr">
      <header class="page-head">
        <div class="page-head-text">
          <h1>Point in time recovery</h1>
          <p class="lead">Bring a collection back as it was at an instant of its window ({window} by default). The copy goes into a new collection; the source is not touched. A rename then puts the copy in place, so the apps change nothing.</p>
        </div>
      </header>

      {notice && <p class="note ok">{notice}</p>}

      {error && <p class="note bad">{error}</p>}

      <Card>
        <CardHeader title="Windows" description="From when each collection can be restored. A dropped or truncated collection stays restorable for its window." />

        {collections.length === 0 ? (
          <Empty title="Nothing to restore yet" description={window === 'off' ? 'PITR is off: set pitr in ndb.yml.' : 'Collections appear here after their first write.'} />
        ) : (
          <Table minWidth={720} label="PITR windows">
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
              {collections.map(item => (
                <tr>
                  <td class="mono strong">
                    {item.collection} {item.dropped && <Badge tone="pink">dropped</Badge>}
                  </td>
                  <td class="mono">{item.window}</td>
                  <td class="mono sub">{when(item.oldest)}</td>
                  <td class="num">{item.snapshots}</td>
                  <td class="num">{formatBytes(item.bytes)}</td>
                  <td>
                    <Button variant="ghost" size="sm" href={`${base}?collection=${encodeURIComponent(item.collection)}#restore`}>
                      Restore
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      {collections.length > 0 && (
        <Card>
          <CardHeader title="Restore a copy" description="The copy holds every write up to the instant, and none after it." />

          <form class="stack" id="restore" method="post" action={`${base}/restore`}>
            <div class="grid-fields">
              <Field id="pitr-collection" label="Collection">
                <select class="input mono" id="pitr-collection" name="collection">
                  {collections.map(item => (
                    <option value={item.collection} selected={item.collection === source}>
                      {item.collection}
                    </option>
                  ))}
                </select>
              </Field>

              <Field id="pitr-until" label="As of (UTC)">
                <Input id="pitr-until" mono type="datetime-local" step="1" name="until" value={until} required />
              </Field>

              <Field id="pitr-into" label="Into (a new collection)">
                <Input id="pitr-into" mono name="into" value={source ? `${source}_restored` : ''} required />
              </Field>
            </div>

            <div class="form-actions">
              <Button variant="primary" type="submit">
                Restore copy
              </Button>
            </div>
          </form>
        </Card>
      )}

      <Card>
        <CardHeader title="Put the copy in place" description="Rename the copy to the name the apps use. With replace, the current collection goes to the trash, where it stays restorable for its window." />

        <form class="stack" method="post" action={`${base}/rename`}>
          <div class="grid-fields">
            <Field id="rename-from" label="Collection">
              <Input id="rename-from" mono name="from" value={renameFrom ?? ''} placeholder="calls_restored" required />
            </Field>

            <Field id="rename-to" label="New name">
              <Input id="rename-to" mono name="to" value={renameTo ?? ''} placeholder="calls" required />
            </Field>
          </div>

          <label class="check">
            <input type="checkbox" name="replace" value="1" checked={Boolean(renameTo)} />
            <span>Replace the collection with that name</span>
          </label>

          <div class="form-actions">
            <Button variant="dark" type="submit">
              Rename
            </Button>
          </div>
        </form>
      </Card>

      <Card>
        <CardHeader title="From the host" description="The same flow with the CLI." />

        <CodeBlock code={CLI} />
      </Card>
    </Layout>
  );
};
