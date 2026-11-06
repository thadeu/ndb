import type { FC } from 'hono/jsx';

import { Layout } from '@/html/layout';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge, Dot } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { Dialog, openDialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { SegmentBar } from '@/ui/progress';
import { Segmented } from '@/ui/segmented';
import { Stat } from '@/ui/stat';
import { Table } from '@/ui/table';
import { Textarea } from '@/ui/textarea';

const Block: FC<{ title: string; children?: unknown }> = ({ title, children }) => (
  <section class="section">
    <h2 class="section-title">{title}</h2>
    <div class="row">{children as never}</div>
  </section>
);

export const UiPage: FC = () => (
  <Layout title="Components">
    <h1>Components</h1>

    <Block title="Button">
      <Button variant="primary">Primary</Button>
      <Button>Secondary</Button>
      <Button variant="dark">Dark</Button>
      <Button variant="ghost">Ghost</Button>
      <Button variant="destructive">Destructive</Button>
      <Button size="sm">Small</Button>
    </Block>

    <Block title="Badge and Dot">
      <Badge>neutral</Badge>
      <Badge tone="pink">direct</Badge>
      <Badge tone="ok">ok</Badge>
      <Dot on />
      <Dot on={false} />
    </Block>

    <Block title="Field and Input">
      <Field id="demo" label="URL">
        <Input id="demo" mono value="tcp://10.10.0.1:7070" />
      </Field>
    </Block>

    <Block title="Stat">
      <Stat label="Rows" value="3.1M" sub="9 collections" />
    </Block>

    <Block title="Card">
      <Card>
        <CardHeader title="Disk" description="/var/lib/ndb" />
        <SegmentBar label="demo" segments={[{ label: 'a', value: 60, tone: 'ink' }, { label: 'b', value: 30, tone: 'pink' }, { label: 'c', value: 10, tone: 'gray' }]} />
      </Card>
    </Block>

    <Block title="Table">
      <div class="card flush">
        <Table minWidth={300}>
          <thead>
            <tr>
              <th>name</th>
              <th class="num">rows</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td class="mono strong">orders</td>
              <td class="num">1,204</td>
            </tr>
          </tbody>
        </Table>
      </div>
    </Block>

    <Block title="Textarea">
      <Textarea mono rows={3} placeholder="{}" aria-label="Demo JSON" />
    </Block>

    <Block title="Segmented">
      <Segmented items={[{ id: 'tree', label: 'Tree' }, { id: 'json', label: 'JSON' }]} value="tree" label="Demo view" />
    </Block>

    <Block title="Dialog and AlertDialog">
      <Button href="ui" {...openDialog('demo-dialog')}>
        Open a dialog
      </Button>

      <form method="get" action="ui" {...confirm({ title: 'Delete this?', body: 'This is a demo; nothing is deleted.', action: 'Delete' })}>
        <Button variant="destructive" type="submit">
          Confirm a delete
        </Button>
      </form>

      <Dialog id="demo-dialog" title="A dialog" description="Opens as a modal with the script, and as a plain page without it." closeHref="ui">
        <p class="hint">Dialog body.</p>
      </Dialog>

      <ConfirmHost />
    </Block>

    <Block title="CodeBlock">
      <CodeBlock code={'ndb backup /backups/daily -gzip'} />
    </Block>

    <Block title="Empty">
      <Card>
        <Empty title="Nothing here" description="Add something to see it." />
      </Card>
    </Block>
  </Layout>
);
