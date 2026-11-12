import type { Context, Hono } from 'hono';

import type { Connection } from '@/connections';
import type { Doc, Ndb } from '@/ndb';
import type { Env } from '@/prefix';
import { ack, append, bury, claimIdle, consumers, createGroup, deadInfo, deadName, entries, groups, nack, pending, replay, streamInfo, streamNames, trim } from '@/streams';
import { OfflinePage } from '@/views/offline';
import { type StreamDialog, type StreamLists, type StreamView, type StreamsQuery, StreamsPage, streamsHref } from '@/views/streams';

import { connectionOf, type RouteDeps } from './context';

const VIEWS: StreamView[] = ['pending', 'entries', 'consumers', 'dead'];
const DIALOGS: StreamDialog[] = ['trim', 'group', 'append', 'claim'];

// The longest wait of a nack that ndb takes: 30 days.
const NACK_MAX_S = 2_592_000;

type Form = Record<string, string>;

async function readForm(c: Context<Env>): Promise<Form> {
  const body = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);

  return Object.fromEntries(Object.entries(body).map(([key, value]) => [key, typeof value === 'string' ? value.trim() : '']));
}

// placeOf is the stream, group and list that a form came from.
function placeOf(form: Form): StreamsQuery {
  return { stream: form.stream || undefined, group: form.group || undefined, view: VIEWS.find(view => view === form.view) };
}

async function loadLists(client: Ndb, stream: string, group: string | undefined, view: StreamView, before?: string): Promise<StreamLists> {
  if (view === 'pending' && group) return { pending: await pending(client, stream, group) };

  if (view === 'consumers' && group) return { consumers: await consumers(client, stream, group) };

  if (view === 'dead') return { dead: await entries(client, deadName(stream), before).catch(() => [] as Doc[]) };

  return { entries: await entries(client, stream, before) };
}

export function streamsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/streams', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);
    const q = c.req.query();

    try {
      const [stats, streams] = await Promise.all([client.stats(), streamNames(client)]);
      const current = deps.currentOf(connection, stats);

      const name = streams.find(stream => stream === q.stream) ?? streams.find(stream => !stream.endsWith('_dead')) ?? streams[0];

      if (!name) return c.html(<StreamsPage current={current} streams={[]} view="entries" lists={{}} query={{}} now={Date.now()} error={q.error} />);

      const [info, list, dead] = await Promise.all([streamInfo(client, name), groups(client, name), deadInfo(client, name)]);

      const group = (list.find(item => item.name === q.group) ?? list[0])?.name;
      const asked = VIEWS.find(view => view === q.view) ?? (group ? 'pending' : 'entries');
      const view = !group && (asked === 'pending' || asked === 'consumers') ? 'entries' : asked;

      const lists = await loadLists(client, name, group, view, q.before);
      const query: StreamsQuery = { stream: name, group, view, dialog: DIALOGS.find(dialog => dialog === q.dialog), nack: q.nack, before: q.before };

      const page = (
        <StreamsPage
          current={current}
          streams={streams}
          picked={{ name, info, groups: list, dead }}
          group={group}
          view={view}
          lists={lists}
          query={query}
          now={Date.now()}
          notice={q.notice}
          error={q.error}
        />
      );

      return c.html(page);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // write adds one POST route. The work gets the form and returns the notice
  // of the page. A failure comes back as the error of the page.
  function write(route: string, work: (client: Ndb, form: Form, place: StreamsQuery) => Promise<string>, reopen?: (form: Form) => StreamsQuery): void {
    app.post(`/c/:name/streams/${route}`, async c => {
      const connection = connectionOf(deps, c);

      if (!connection) return c.text('Not found', 404);

      const form = await readForm(c);
      const place = placeOf(form);

      if (!place.stream) return backTo(c, connection, place, { error: 'The form has no stream.' });

      try {
        const notice = await work(deps.client(connection), form, place);

        return backTo(c, connection, place, { notice });
      } catch (error) {
        const message = error instanceof Refused ? error.message : deps.message(error);

        return backTo(c, connection, { ...place, ...reopen?.(form) }, { error: message });
      }
    });
  }

  write(
    'trim',
    async (client, form, place) => {
      const body: Record<string, unknown> = {};

      if (form.max_len) body.max_len = wholeNumber(form.max_len, 'max_len');
      if (form.min_id) body.min_id = form.min_id;

      if (Object.keys(body).length !== 1) throw new Refused('Give max_len or min_id, one of the two.');

      if (form.approx === '1' || form.approx === 'on') body.approx = true;

      const answer = await trim(client, place.stream!, body);

      return `Trim deleted ${answer.deleted} ${answer.deleted === 1 ? 'entry' : 'entries'} of ${place.stream}.`;
    },
    () => ({ dialog: 'trim' }),
  );

  write(
    'group',
    async (client, form, place) => {
      if (!form.name) throw new Refused('Give the group a name.');

      const answer = await createGroup(client, place.stream!, form.name, form.start_id || form.start || '$');

      place.group = answer.group;
      place.view = 'pending';

      return `The group ${answer.group} starts after ${answer.last_delivered_id}.`;
    },
    () => ({ dialog: 'group' }),
  );

  write(
    'append',
    async (client, form, place) => {
      const made = await append(client, place.stream!, objectOf(form.doc ?? ''));

      return `Appended ${made._id} to ${place.stream}.`;
    },
    () => ({ dialog: 'append' }),
  );

  write('ack', async (client, form, place) => {
    const answer = await ack(client, place.stream!, needGroup(place), [needId(form)]);

    return answer.acked === 1 ? `Acked ${form.id}.` : `${form.id} was not pending.`;
  });

  write(
    'nack',
    async (client, form, place) => {
      const delay = wholeNumber(form.delay_s || '0', 'The wait');

      if (delay > NACK_MAX_S) throw new Refused('The wait is at most 30 days.');

      const answer = await nack(client, place.stream!, needGroup(place), needId(form), delay * 1000);

      return answer.nacked === 1 ? `Nacked ${form.id}. It comes back in ${delay} s.` : `${form.id} was not pending for its consumer.`;
    },
    form => ({ nack: form.id }),
  );

  write('bury', async (client, form, place) => {
    const dead = await bury(client, place.stream!, needGroup(place), needId(form));

    return `${form.id} is in ${dead} and left the group.`;
  });

  write(
    'claim',
    async (client, form, place) => {
      if (!form.consumer) throw new Refused('Give the consumer that takes the entries.');

      const idle = wholeNumber(form.min_idle_s || '0', 'The idle time');
      const claimed = await claimIdle(client, place.stream!, needGroup(place), form.consumer, idle * 1000);

      return `${form.consumer} claimed ${claimed} ${claimed === 1 ? 'entry' : 'entries'}.`;
    },
    () => ({ dialog: 'claim' }),
  );

  write('replay', async (client, form, place) => {
    const id = await replay(client, place.stream!, needId(form));

    return `Appended again as ${id}. The dead entry is deleted.`;
  });
}

// Refused is a form that the console does not send to ndb.
class Refused extends Error {}

function wholeNumber(text: string, label: string): number {
  const value = Number(text);

  if (!Number.isInteger(value) || value < 0) throw new Refused(`${label} must be a whole number, 0 or more.`);

  return value;
}

function objectOf(text: string): Doc {
  let value: unknown;

  try {
    value = JSON.parse(text);
  } catch {
    throw new Refused('The entry is not valid JSON.');
  }

  if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new Refused('The entry must be a JSON object.');

  return value as Doc;
}

function needGroup(place: StreamsQuery): string {
  if (!place.group) throw new Refused('The form has no group.');

  return place.group;
}

function needId(form: Form): string {
  if (!form.id) throw new Refused('The form has no entry ID.');

  return form.id;
}

function backTo(c: Context<Env>, connection: Connection, place: StreamsQuery, params: Record<string, string>) {
  const href = streamsHref(connection.name, place);
  const glue = href.includes('?') ? '&' : '?';

  return c.redirect(`${c.var.prefix}/${href}${glue}${new URLSearchParams(params)}`, 303);
}
