import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import type { CollectionStats } from '@/ndb';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { Empty } from '@/ui/empty';
import { Input } from '@/ui/input';

export interface WatchPageProps {
  current: Current;
  collections: CollectionStats[];
  coll: string;
  where: string;
}

export const WatchPage: FC<WatchPageProps> = ({ current, collections, coll, where }) => {
  const action = `c/${encodeURIComponent(current.name)}/watch`;
  const props = { coll, where, streamUrl: `${action}/stream` };
  const direct = collections.find(item => item.name === coll)?.write === 'direct';

  return (
    <Layout title="Watch" current={current} tab="watch" scripts={['watch']}>
      <header class="page-head">
        <div class="page-head-text">
          <h1>Watch</h1>
          <p class="lead">Tail the changes of a collection as they happen. The console only reads: it never acks.</p>
        </div>
      </header>

      <form class="watch-form" method="get" action={action}>
        <div class="watch-field">
          <label class="label" for="watch-coll">
            Collection
          </label>
          <select class="input mono" id="watch-coll" name="coll">
            {collections.map(item => (
              <option value={item.name} selected={item.name === coll}>
                {item.name}
              </option>
            ))}
          </select>
        </div>

        <div class="watch-field grow">
          <label class="label" for="watch-where">
            where
          </label>
          <Input id="watch-where" mono name="where" value={where} placeholder='{"status":"failed"}' spellcheck={false} />
        </div>

        <Button variant="dark" type="submit">
          Apply
        </Button>
      </form>

      {collections.length === 0 && (
        <Card>
          <Empty title="No collections yet" description="Insert a document and the collection appears here." />
        </Card>
      )}

      {collections.length > 0 && direct && (
        <Card>
          <Empty title="Direct collections have no watch" description="ndb reads the files of another app in direct mode, so there is no change feed to tail." />
        </Card>
      )}

      {collections.length > 0 && !direct && (
        <div id="watch" data-props={JSON.stringify(props)}>
          <Card>
            <Empty title="Live tail needs JavaScript" description="Turn on JavaScript to start the stream. The filter above is kept in the URL." />
          </Card>
        </div>
      )}

      <p class="hint">
        Each stream opens a watch with a consumer named <span class="mono">ndb-ui-xxxxxxxx</span>. ndb stores a position row for every consumer name, and this tail never acks, so the row stays at the start point of the stream. With{' '}
        <span class="mono">wal_retention</span> set, the WAL files from that point are kept. ndb has no call to remove a consumer yet.
      </p>
    </Layout>
  );
};
