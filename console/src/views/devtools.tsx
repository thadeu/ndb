import type { FC } from 'hono/jsx';

import { defaultText } from '@/components/devtools-blocks';
import { Layout, type Current } from '@/html/layout';
import { Card } from '@/ui/card';
import { Code } from '@/ui/code';

export interface DevtoolsPageProps {
  current: Current;
  collection: string;
  collections?: string[];
  curl: { kind: 'unix' | 'tcp'; socket?: string; base?: string };
}

export const DevtoolsPage: FC<DevtoolsPageProps> = ({ current, collection, collections = [], curl }) => {
  const base = `c/${encodeURIComponent(current.name)}`;

  const props = {
    name: current.name,
    collection,
    collections,
    sendUrl: `${base}/devtools/send`,
    curl,
    browserHref: `${base}/explorer?${new URLSearchParams({ coll: collection, tab: 'documents' })}`,
  };

  return (
    <Layout title="Dev Tools" current={current} tab="devtools" scripts={['devtools']}>
      <div id="devtools" data-props={JSON.stringify(props)}>
        <div class="dt">
          <header class="dt-bar">
            <h1 class="dt-title">Dev Tools</h1>
            <span class="hint">Send any request of the HTTP API. Start from an example, then change it.</span>
          </header>

          <Card>
            <h2 class="card-title">Dev Tools needs JavaScript</h2>
            <p class="hint">Write a request as a line with the method and the path, then a JSON body. Run it with Ctrl+Enter or Cmd+Enter. These are the first examples:</p>

            <pre class="codebox-pre">
              <Code text={defaultText(collection)} />
            </pre>
          </Card>
        </div>
      </div>
    </Layout>
  );
};
