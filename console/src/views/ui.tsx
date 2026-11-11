import type { FC } from 'hono/jsx';

import { Layout } from '@/html/layout';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge, Dot } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { DatePicker } from '@/ui/date-picker';
import { Dialog, openDialog } from '@/ui/dialog';
import { DataTable } from '@/ui/data-table';
import { DescriptionList } from '@/ui/description-list';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Icon, type IconName } from '@/ui/icon';
import { Input } from '@/ui/input';
import { Pagination } from '@/ui/pagination';
import { Popover, popoverTrigger } from '@/ui/popover';
import { SegmentBar } from '@/ui/progress';
import { Segmented } from '@/ui/segmented';
import { Select } from '@/ui/select';
import { Stat } from '@/ui/stat';
import { Table } from '@/ui/table';
import { Tabs } from '@/ui/tabs';
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

    <Block title="DatePicker">
      <div class="stack" style="width:min(420px,100%)">
        <Field id="demo-dt" label="Date and time (UTC), with presets">
          <DatePicker id="demo-dt" name="at" label="Date and time" max="now" presets={[{ label: '5 minutes ago', ago: '5m' }, { label: '1 hour ago', ago: '1h' }, { label: '1 day ago', ago: '1d' }]} />
        </Field>

        <Field id="demo-d" label="Date">
          <DatePicker id="demo-d" name="day" label="Date" mode="date" />
        </Field>

        <Field id="demo-r" label="Range, with presets">
          <DatePicker id="demo-r" name="period" label="Range" mode="range" max="now" presets={[{ label: 'Last day', last: '1d' }, { label: 'Last 7 days', last: '7d' }, { label: 'Last 30 days', last: '30d' }, { label: 'Last 3 months', last: '3mo' }]} />
        </Field>
      </div>
    </Block>

    <Block title="Popover">
      <Button {...popoverTrigger('demo-pop')}>Open popover</Button>

      <Popover id="demo-pop" label="Demo">
        <p class="hint" style="padding:12px;margin:0">Below the trigger when it fits, above when not; inside the viewport.</p>
      </Popover>
    </Block>

    <Block title="Tabs">
      <div style="width:min(520px,100%)">
        <Tabs id="demo-tabs" label="Install" items={[{ id: 'cli', label: 'CLI', content: <CodeBlock code="ndb stats" /> }, { id: 'docker', label: 'Docker', content: <CodeBlock code="docker exec ndb ndb stats" /> }]} />
      </div>
    </Block>

    <Block title="CodeBlock">
      <CodeBlock code={'ndb backup /backups/daily -gzip'} />
    </Block>

    <Block title="Icon">
      {(['refresh', 'plus', 'x', 'chevron-down', 'chevron-right', 'chevron-left', 'chevrons-left', 'arrow-left', 'arrow-up', 'arrow-down', 'columns', 'documents'] as IconName[]).map(name => (
        <span class="badge" title={name}>
          <Icon name={name} />
        </span>
      ))}
    </Block>

    <Block title="Select">
      <Select
        name="op"
        value="gte"
        aria-label="Condition"
        options={[
          { id: 'eq', label: 'Equal to' },
          { id: 'gte', label: 'Greater than or equal to' },
        ]}
      />
    </Block>

    <Block title="DataTable and Pagination">
      <div class="card flush dtable-card">
        <DataTable
          id="gallery"
          label="Example"
          columns={[
            { id: '_id', label: '_id', href: 'ui', sort: 'desc' },
            { id: 'status', label: 'status', href: 'ui' },
            { id: 'total', label: 'total' },
          ]}
          title={<h2 class="card-title">Orders</h2>}
          footer={<Pagination page={2} from={26} to={50} total={812} firstHref="ui" prevHref="ui" nextHref="ui" />}
        >
          <tr>
            <td data-col="_id">a1</td>
            <td data-col="status">paid</td>
            <td data-col="total">120</td>
          </tr>

          <tr>
            <td data-col="_id">a2</td>
            <td data-col="status">new</td>
            <td data-col="total">80</td>
          </tr>
        </DataTable>
      </div>
    </Block>

    <Block title="DescriptionList">
      <Card>
        <DescriptionList items={[{ label: 'Write mode', value: 'engine' }, { label: 'Documents', value: '557,598' }, { label: 'Size', value: '96 MB' }, { label: 'PITR', value: 'On' }]} />
      </Card>
    </Block>

    <Block title="Empty">
      <Card>
        <Empty title="Nothing here" description="Add something to see it." />
      </Card>
    </Block>
  </Layout>
);
