import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { Empty } from '@/ui/empty';

export const OfflinePage: FC<{ current: Current; message: string }> = ({ current, message }) => (
  <Layout title="Offline" current={current}>
    <Card>
      <Empty
        title={`${current.name} is not reachable`}
        description={message}
        action={
          <Button variant="dark" href="./">
            Back to connections
          </Button>
        }
      />
    </Card>
  </Layout>
);
