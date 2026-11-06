import type { FC } from 'hono/jsx';

import { defaultText } from '@/components/devtools-blocks';
import { Layout, type Current } from '@/html/layout';
import { Card } from '@/ui/card';

export interface DevtoolsPageProps {
  current: Current;
  collection: string;
  curl: { kind: 'unix' | 'tcp'; socket?: string; base?: string };
}

export const DevtoolsPage: FC<DevtoolsPageProps> = ({ current, collection, curl }) => {
  const props = { name: current.name, collection, sendUrl: `c/${encodeURIComponent(current.name)}/devtools/send`, curl, browserHref: `c/${encodeURIComponent(current.name)}/browser` };

  return (
    <Layout title="Dev Tools" current={current} tab="devtools" scripts={['devtools']} bleed>
      <div id="devtools" data-props={JSON.stringify(props)}>
        <div class="dt-fallback">
          <Card>
            <h2 class="card-title">Dev Tools needs JavaScript</h2>
            <p class="hint">Write a request as a line with the method and the path, then a JSON body. Run it with Ctrl+Enter or Cmd+Enter. These are the first examples:</p>

            <pre class="codebox-pre">
              <code>{defaultText(collection)}</code>
            </pre>
          </Card>
        </div>
      </div>
    </Layout>
  );
};
