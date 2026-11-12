import type { Child, FC } from 'hono/jsx';

import { formatCount } from '@/format';
import { Layout, type Current } from '@/html/layout';
import type { Doc } from '@/ndb';
import { type Consumer, type Group, type PendingEntry, type StreamInfo, PAGE, deadName, formatMs, payloadOf, progressOf, stateOf, totalsOf } from '@/streams';
import { ConfirmHost, confirm } from '@/ui/alert-dialog';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card, CardHeader } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { Dialog, openDialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';
import { NavTabs } from '@/ui/nav-tabs';
import { Select } from '@/ui/select';
import { Stat } from '@/ui/stat';
import { Table } from '@/ui/table';
import { Textarea } from '@/ui/textarea';

export type StreamView = 'pending' | 'entries' | 'consumers' | 'dead';

export type StreamDialog = 'trim' | 'group' | 'append' | 'claim';

// StreamsQuery is the state of the page in the URL: the stream, the group,
// the list of the group, and the dialog that is open.
export interface StreamsQuery {
  stream?: string;
  group?: string;
  view?: StreamView;
  dialog?: StreamDialog;
  nack?: string;
  before?: string;
}

export interface Picked {
  name: string;
  info: StreamInfo;
  groups: Group[];
  dead?: StreamInfo;
}

export interface StreamLists {
  pending?: PendingEntry[];
  entries?: Doc[];
  consumers?: Consumer[];
  dead?: Doc[];
}

export interface StreamsPageProps {
  current: Current;
  streams: string[];
  picked?: Picked;
  group?: string;
  view: StreamView;
  lists: StreamLists;
  query: StreamsQuery;
  now: number;
  notice?: string;
  error?: string;
}

export function streamsHref(name: string, query: StreamsQuery = {}): string {
  const params = new URLSearchParams();

  if (query.stream) params.set('stream', query.stream);
  if (query.group) params.set('group', query.group);
  if (query.view) params.set('view', query.view);
  if (query.dialog) params.set('dialog', query.dialog);
  if (query.nack) params.set('nack', query.nack);
  if (query.before) params.set('before', query.before);

  const search = params.toString();

  return `c/${encodeURIComponent(name)}/streams${search ? `?${search}` : ''}`;
}

const HOW_TO = ['# make the stream', `curl --unix-socket /run/ndb/ndb.sock -X PUT http://ndb/jobs -d '{"stream": true}'`, '', '# or append the first entry: it makes the stream too', `curl --unix-socket /run/ndb/ndb.sock http://ndb/jobs/_streams/append -d '{"doc": {"task": "send"}}'`].join('\n');

const docText = (doc: unknown) => JSON.stringify(doc);

// Where is the place of a form in the page: the hidden fields send it, so
// the answer comes back to the same stream, group and list.
const Where: FC<{ query: StreamsQuery }> = ({ query }) => (
  <>
    <input type="hidden" name="stream" value={query.stream ?? ''} />
    <input type="hidden" name="group" value={query.group ?? ''} />
    <input type="hidden" name="view" value={query.view ?? ''} />
  </>
);

interface RowActionProps {
  action: string;
  query: StreamsQuery;
  id: string;
  label: string;
  danger?: boolean;
  ask?: { title: string; body: string };
}

const RowAction: FC<RowActionProps> = ({ action, query, id, label, danger = false, ask }) => {
  const guard = ask ? confirm({ title: ask.title, body: ask.body, action: label, tone: danger ? 'danger' : 'default' }) : {};

  return (
    <form class="stream-row-form" method="post" action={action} {...guard}>
      <Where query={query} />
      <input type="hidden" name="id" value={id} />

      <Button variant={danger ? 'destructive' : 'ghost'} size="sm" type="submit">
        {label}
      </Button>
    </form>
  );
};

const GroupsCard: FC<{ current: Current; picked: Picked; group?: string; view: StreamView }> = ({ current, picked, group, view }) => (
  <Card class="flush">
    <div class="stream-card-head">
      <h2 class="card-title">Consumer groups</h2>
    </div>

    {picked.groups.length === 0 && (
      <div class="stream-card-body">
        <Empty title="No group yet" description="A group reads the whole stream. Each entry goes to one consumer of the group. Select New group to make one." />
      </div>
    )}

    {picked.groups.length > 0 && (
      <Table minWidth={720} label="Consumer groups">
        <thead>
          <tr>
            <th>group</th>
            <th class="num">consumers</th>
            <th class="num">pending</th>
            <th class="num">lag</th>
            <th>last delivered</th>
            <th>progress</th>
          </tr>
        </thead>

        <tbody>
          {picked.groups.map(item => {
            const done = progressOf(item, picked.info);
            const next = view === 'entries' || view === 'dead' ? 'pending' : view;

            return (
              <tr class={item.name === group ? 'stream-row on' : 'stream-row'}>
                <td class="mono">
                  <a class="stream-group-link" href={streamsHref(current.name, { stream: picked.name, group: item.name, view: next })}>
                    {item.name}
                  </a>
                </td>
                <td class="num">{item.consumers}</td>
                <td class="num stream-strong">{item.pending}</td>
                <td class="num">{item.lag === null ? 'unknown' : item.lag}</td>
                <td class="mono">{item.last_delivered_id}</td>
                <td>
                  {done === undefined ? (
                    <span class="hint">unknown</span>
                  ) : (
                    <span class="stream-progress" role="img" aria-label={`${done}% read`}>
                      <span class="stream-progress-fill" style={`width:${done}%`} />
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </Table>
    )}
  </Card>
);

interface ListProps {
  current: Current;
  picked: Picked;
  query: StreamsQuery;
  lists: StreamLists;
  now: number;
}

const PendingList: FC<ListProps> = ({ current, picked, query, lists, now }) => {
  const rows = lists.pending ?? [];
  const base = `c/${encodeURIComponent(current.name)}/streams`;

  if (rows.length === 0) return <ListEmpty title="Nothing pending" description="Each entry that the group took is acked." />;

  return (
    <Table minWidth={820} label="Pending entries">
      <thead>
        <tr>
          <th>id</th>
          <th>consumer</th>
          <th class="num">deliveries</th>
          <th>idle</th>
          <th>state</th>
          <th>
            <span class="sr-only">actions</span>
          </th>
        </tr>
      </thead>

      <tbody>
        {rows.map(entry => {
          const state = stateOf(entry, now);

          return (
            <tr>
              <td class="mono stream-strong">{entry._id}</td>
              <td class="mono">{entry.consumer === '' ? 'none' : entry.consumer}</td>
              <td class="num">{entry.deliveries}</td>
              <td>{formatMs(entry.idle_ms)}</td>
              <td>
                <Badge tone={state.tone}>{state.label}</Badge>
              </td>
              <td class="stream-actions">
                <RowAction action={`${base}/ack`} query={query} id={entry._id} label="Ack" />

                {entry.consumer !== '' && (
                  <Button variant="ghost" size="sm" href={streamsHref(current.name, { ...query, nack: entry._id })}>
                    Nack with delay
                  </Button>
                )}

                <RowAction
                  action={`${base}/bury`}
                  query={query}
                  id={entry._id}
                  label="To dead letter"
                  danger
                  ask={{ title: 'Send this entry to the dead letter?', body: `The entry ${entry._id} goes to ${deadName(picked.name)} and leaves the group.` }}
                />
              </td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
};

const EntriesList: FC<ListProps> = ({ current, query, lists }) => {
  const rows = lists.entries ?? [];
  const last = rows[rows.length - 1];

  if (rows.length === 0) return <ListEmpty title="No entry" description="Select Append entry to add one." />;

  return (
    <>
      <Table minWidth={720} label="Entries">
        <thead>
          <tr>
            <th>id</th>
            <th>time</th>
            <th>entry</th>
          </tr>
        </thead>

        <tbody>
          {rows.map(entry => (
            <tr>
              <td class="mono stream-strong">{String(entry._id)}</td>
              <td class="mono">{String(entry._ts ?? '')}</td>
              <td class="mono stream-doc">{docText(payloadOf(entry))}</td>
            </tr>
          ))}
        </tbody>
      </Table>

      {rows.length === PAGE && last && (
        <div class="stream-card-body">
          <Button size="sm" href={streamsHref(current.name, { ...query, before: String(last._id) })}>
            Older entries
          </Button>
        </div>
      )}
    </>
  );
};

const ConsumersList: FC<ListProps> = ({ lists }) => {
  const rows = lists.consumers ?? [];

  if (rows.length === 0) return <ListEmpty title="No consumer" description="A consumer is made on its first read." />;

  return (
    <Table minWidth={560} label="Consumers">
      <thead>
        <tr>
          <th>consumer</th>
          <th class="num">pending</th>
          <th>idle</th>
          <th>last entry</th>
        </tr>
      </thead>

      <tbody>
        {rows.map(consumer => (
          <tr>
            <td class="mono stream-strong">{consumer.name}</td>
            <td class="num">{consumer.pending}</td>
            <td>{formatMs(consumer.idle_ms)}</td>
            <td>{formatMs(consumer.inactive_ms)} ago</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
};

const DeadList: FC<ListProps> = ({ current, picked, query, lists }) => {
  const rows = lists.dead ?? [];
  const action = `c/${encodeURIComponent(current.name)}/streams/replay`;

  if (rows.length === 0) return <ListEmpty title="The dead letter is empty" description={`No entry is in ${deadName(picked.name)}.`} />;

  return (
    <Table minWidth={860} label="Dead letter entries">
      <thead>
        <tr>
          <th>id</th>
          <th>source id</th>
          <th>group</th>
          <th class="num">deliveries</th>
          <th>reason</th>
          <th>doc</th>
          <th>
            <span class="sr-only">actions</span>
          </th>
        </tr>
      </thead>

      <tbody>
        {rows.map(entry => (
          <tr>
            <td class="mono stream-strong">{String(entry._id)}</td>
            <td class="mono">{String(entry.id ?? '')}</td>
            <td class="mono">{String(entry.group ?? '')}</td>
            <td class="num">{String(entry.deliveries ?? '')}</td>
            <td>{entry.reason === undefined ? '' : <Badge tone="warn">{String(entry.reason)}</Badge>}</td>
            <td class="mono stream-doc">{docText(entry.doc ?? payloadOf(entry))}</td>
            <td class="stream-actions">
              <RowAction
                action={action}
                query={query}
                id={String(entry._id)}
                label="Append again"
                ask={{ title: 'Append this entry again?', body: `Its doc goes to ${picked.name} as a new entry, and the dead entry is deleted.` }}
              />
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
};

const ListEmpty: FC<{ title: string; description: string }> = ({ title, description }) => (
  <div class="stream-card-body">
    <Empty title={title} description={description} />
  </div>
);

const ListsCard: FC<ListProps & { group?: string; view: StreamView }> = props => {
  const { current, picked, query, group, view } = props;
  const selected = picked.groups.find(item => item.name === group);
  const dead = picked.dead?.length ?? 0;
  const hrefOf = (next: StreamView) => streamsHref(current.name, { stream: picked.name, group, view: next });

  const tabs = [
    ...(selected ? [{ id: 'pending', label: `Pending (${selected.pending})`, href: hrefOf('pending') }] : []),
    { id: 'entries', label: 'Entries', href: hrefOf('entries') },
    ...(selected ? [{ id: 'consumers', label: 'Consumers', href: hrefOf('consumers') }] : []),
    { id: 'dead', label: `Dead letter (${dead})`, href: hrefOf('dead') },
  ];

  return (
    <Card class="flush">
      <div class="stream-lists-head">
        <NavTabs items={tabs} value={view} label={selected ? `Entries of the group ${selected.name}` : 'Entries of the stream'} />

        <span class="agg-gap" />

        {selected && (
          <span class="hint">
            group <strong class="mono stream-group-name">{selected.name}</strong>
          </span>
        )}

        {selected && (
          <Button size="sm" href={streamsHref(current.name, { ...query, dialog: 'claim' })} {...openDialog('stream-claim')}>
            Claim idle
          </Button>
        )}
      </div>

      {view === 'pending' && <PendingList {...props} />}

      {view === 'entries' && <EntriesList {...props} />}

      {view === 'consumers' && <ConsumersList {...props} />}

      {view === 'dead' && <DeadList {...props} />}

      <p class="hint stream-foot">
        An entry that fails on its last allowed delivery goes to <code class="mono">{deadName(picked.name)}</code>. Open Dead letter to read it and to append it again.
      </p>
    </Card>
  );
};

interface FormDialogProps {
  id: string;
  title: string;
  description: string;
  open: boolean;
  action: string;
  submit: string;
  query: StreamsQuery;
  closeHref: string;
  children?: Child;
}

const FormDialog: FC<FormDialogProps> = ({ id, title, description, open, action, submit, query, closeHref, children }) => (
  <Dialog id={id} title={title} description={description} open={open} closeHref={closeHref}>
    <form class="stack" method="post" action={action}>
      <Where query={query} />

      {children}

      <div class="form-actions">
        <Button variant="primary" type="submit">
          {submit}
        </Button>

        <Button href={closeHref} data-dialog-close>
          Cancel
        </Button>
      </div>
    </form>
  </Dialog>
);

const Dialogs: FC<{ current: Current; picked: Picked; group?: string; query: StreamsQuery }> = ({ current, picked, group, query }) => {
  const base = `c/${encodeURIComponent(current.name)}/streams`;
  const place = { stream: picked.name, group, view: query.view };
  const closeHref = streamsHref(current.name, place);

  return (
    <>
      <FormDialog
        id="stream-trim"
        title={`Trim ${picked.name}`}
        description="Deletes the oldest entries. A trim does not change the groups."
        open={query.dialog === 'trim'}
        action={`${base}/trim`}
        submit="Trim"
        query={place}
        closeHref={closeHref}
      >
        <Field id="trim-max-len" label="Keep the newest entries (max_len)">
          <Input id="trim-max-len" name="max_len" type="number" min="0" inputmode="numeric" placeholder="10000" />
        </Field>

        <Field id="trim-min-id" label="Or delete the entries before this ID (min_id)">
          <Input id="trim-min-id" name="min_id" mono placeholder={picked.info.first_id ?? '1731160000000-0'} />
        </Field>

        <label class="check">
          <input type="checkbox" name="approx" value="1" />
          <span>Approximate: delete only whole blocks of 1 024 entries</span>
        </label>
      </FormDialog>

      <FormDialog
        id="stream-group"
        title="New group"
        description={`A consumer group of ${picked.name}.`}
        open={query.dialog === 'group'}
        action={`${base}/group`}
        submit="Make the group"
        query={place}
        closeHref={closeHref}
      >
        <Field id="group-name" label="Name">
          <Input id="group-name" name="name" mono required placeholder="workers" />
        </Field>

        <Field id="group-start" label="Start">
          <Select
            id="group-start"
            name="start"
            value="$"
            options={[
              { id: '$', label: 'Only new entries ($)' },
              { id: '0', label: 'Every entry (0)' },
            ]}
          />
        </Field>

        <Field id="group-start-id" label="Or start after this ID">
          <Input id="group-start-id" name="start_id" mono placeholder={picked.info.last_id ?? '1731160000000-0'} />
        </Field>
      </FormDialog>

      <FormDialog
        id="stream-append"
        title="Append entry"
        description={`A new entry of ${picked.name}. ndb makes its ID.`}
        open={query.dialog === 'append'}
        action={`${base}/append`}
        submit="Append"
        query={place}
        closeHref={closeHref}
      >
        <Field id="append-doc" label="Entry (a JSON object)">
          <Textarea id="append-doc" name="doc" mono rows={6} required>
            {'{\n  \n}'}
          </Textarea>
        </Field>
      </FormDialog>

      {group && (
        <FormDialog
          id="stream-claim"
          title="Claim idle"
          description={`Moves the idle pending entries of the group ${group} to one consumer. Each one gets one more delivery.`}
          open={query.dialog === 'claim'}
          action={`${base}/claim`}
          submit="Claim"
          query={place}
          closeHref={closeHref}
        >
          <Field id="claim-consumer" label="Consumer that takes them">
            <Input id="claim-consumer" name="consumer" mono required placeholder="worker-1" />
          </Field>

          <Field id="claim-idle" label="Idle for at least (seconds)">
            <Input id="claim-idle" name="min_idle_s" type="number" min="0" inputmode="numeric" value="60" />
          </Field>
        </FormDialog>
      )}

      {group && query.nack && (
        <FormDialog
          id="stream-nack"
          title="Nack with delay"
          description={`Gives the entry ${query.nack} back to the group ${group}. Any consumer takes it after the wait.`}
          open
          action={`${base}/nack`}
          submit="Nack"
          query={place}
          closeHref={closeHref}
        >
          <input type="hidden" name="id" value={query.nack} />

          <Field id="nack-delay" label="Wait (seconds)">
            <Input id="nack-delay" name="delay_s" type="number" min="0" max="2592000" inputmode="numeric" value="30" />
          </Field>
        </FormDialog>
      )}
    </>
  );
};

export const StreamsPage: FC<StreamsPageProps> = ({ current, streams, picked, group, view, lists, query, now, notice, error }) => {
  if (!picked) {
    return (
      <Layout title="Streams" current={current} tab="streams">
        <header class="page-head">
          <div class="page-head-text">
            <h1>Streams</h1>
            <p class="lead">An append-only log with consumer groups, like Redis Streams.</p>
          </div>
        </header>

        {error && <p class="note bad">{error}</p>}

        <Card>
          <CardHeader title="No stream yet" />

          <Empty title="Make a stream" description='A stream is a collection made with "stream": true. The first append makes it too.' />

          <CodeBlock code={HOW_TO} />
        </Card>
      </Layout>
    );
  }

  const totals = totalsOf(picked.groups);
  const dead = picked.dead?.length ?? 0;
  const place = { stream: picked.name, group, view };

  return (
    <Layout title={`${picked.name} · Streams`} current={current} tab="streams">
      {streams.length > 1 && (
        <nav class="stream-picks" aria-label="Streams">
          {streams.map(name => (
            <Button
              size="sm"
              class={name === picked.name ? 'stream-pick mono on' : 'stream-pick mono'}
              href={streamsHref(current.name, { stream: name })}
              aria-current={name === picked.name ? 'page' : undefined}
            >
              {name}
            </Button>
          ))}
        </nav>
      )}

      <header class="page-head">
        <div class="page-head-text">
          <span class="hint">Streams / {picked.name}</span>

          <div class="stream-title">
            <h1 class="mono stream-name">{picked.name}</h1>
            <Badge>stream</Badge>
            {dead > 0 && <Badge tone="warn">{formatCount(dead)} in dead letter</Badge>}
          </div>
        </div>

        <div class="stream-head-actions">
          <Button href={streamsHref(current.name, { ...place, dialog: 'trim' })} {...openDialog('stream-trim')}>
            Trim
          </Button>

          <Button href={streamsHref(current.name, { ...place, dialog: 'group' })} {...openDialog('stream-group')}>
            New group
          </Button>

          <Button variant="dark" href={streamsHref(current.name, { ...place, dialog: 'append' })} {...openDialog('stream-append')}>
            Append entry
          </Button>
        </div>
      </header>

      {notice && <p class="note ok">{notice}</p>}

      {error && <p class="note bad">{error}</p>}

      <section class="grid-stats stream-stats" aria-label="Stream totals">
        <Stat label="Entries" value={formatCount(picked.info.length)} sub="append-only log" />
        <Stat label="Groups" value={String(picked.groups.length)} sub={`${totals.consumers} ${totals.consumers === 1 ? 'consumer' : 'consumers'}`} />
        <Stat label="Pending" value={formatCount(totals.pending)} sub="delivered, not acked" />
        <Stat label="Lag" value={totals.lag === null ? 'unknown' : formatCount(totals.lag)} sub="not delivered yet" />
        <Stat label="Dead letter" value={formatCount(dead)} sub={deadName(picked.name)} />
      </section>

      <GroupsCard current={current} picked={picked} group={group} view={view} />

      <ListsCard
        current={current}
        picked={picked}
        query={place}
        lists={lists}
        now={now}
        group={group}
        view={view}
      />

      <Dialogs current={current} picked={picked} group={group} query={{ ...query, view }} />

      <ConfirmHost />
    </Layout>
  );
};
