import type { FC } from 'hono/jsx';

import { Watch, type WatchProps } from '@/components/watch';
import type { WatchSettings } from '@/components/watch-model';
import { Layout, type Current } from '@/html/layout';
import type { CollectionStats } from '@/ndb';
import { Card } from '@/ui/card';
import { Empty } from '@/ui/empty';

export interface WatchPageProps {
  current: Current;
  collections: CollectionStats[];
  settings: WatchSettings;
}

export const WatchPage: FC<WatchPageProps> = ({ current, collections, settings }) => {
  const base = `c/${encodeURIComponent(current.name)}`;

  const props: WatchProps = {
    action: `${base}/watch`,
    streamUrl: `${base}/watch/stream`,
    explorerUrl: `${base}/explorer`,
    streamsHref: `${base}/streams`,
    collections: collections.map(item => ({ name: item.name, direct: item.write === 'direct' })),
    settings,
  };

  return (
    <Layout title="Watch" current={current} tab="watch" scripts={['watch']}>
      <header class="page-head">
        <div class="page-head-text">
          <h1>Watch</h1>
          <p class="lead">The change feed of a collection, live. Delivery to a consumer is at least once.</p>
        </div>
      </header>

      {collections.length === 0 ? (
        <Card>
          <Empty title="No collections yet" description="Insert a document and the collection appears here." />
        </Card>
      ) : (
        <div id="watch" data-props={JSON.stringify(props)}>
          <Watch {...props} live={false} />
        </div>
      )}
    </Layout>
  );
};
