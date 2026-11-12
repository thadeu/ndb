import type { Hono } from 'hono';

import { COUNT_STAGES_MAX, DEFAULT_PIPELINE, type Stage, aggregatePath, countRequest, formatPipeline, hasSealed, isMultiTarget, matchText, parsePipeline, toPipeline, validTarget } from '@/aggregate';
import { type Definition, type Doc, type Ndb, NdbError, type Stats, target } from '@/ndb';
import type { Env } from '@/prefix';
import type { ComboOption } from '@/ui/combobox';
import { AggregatePage, type AggregateResult, type AggregateView, type TargetMatch } from '@/views/aggregate';
import { OfflinePage } from '@/views/offline';

import { connectionOf, type RouteDeps } from './context';

// The most rows that the page asks for. X-Ndb-Total has the full count.
const ROWS = 200;

const VIEWS: AggregateView[] = ['chart', 'table', 'json'];

interface Answer {
  rows: Doc[];
  total?: number;
  examined?: number;
}

// send runs one _aggregate body and reads the rows and the X-Ndb-* headers.
async function send(client: Ndb, on: string, body: Record<string, unknown>, decrypt = false): Promise<Answer> {
  const response = await client.forward('POST', aggregatePath(on, decrypt), JSON.stringify(body));
  const parsed = await response.json().catch(() => null);

  if (!response.ok) {
    const failure = (parsed as { error?: { code?: unknown; message?: unknown } } | null)?.error;

    throw new NdbError(String(failure?.code ?? 'http_error'), String(failure?.message ?? `ndb answered ${response.status}.`), response.status);
  }

  const total = response.headers.get('x-ndb-total');
  const examined = response.headers.get('x-ndb-examined');

  return {
    rows: Array.isArray(parsed) ? (parsed as Doc[]) : [],
    total: total === null ? undefined : Number(total),
    examined: examined === null ? undefined : Number(examined),
  };
}

// countStage gives the rows that leave one stage, or undefined when ndb
// refuses the prefix.
async function countStage(client: Ndb, on: string, stages: Stage[], index: number): Promise<number | undefined> {
  const request = countRequest(stages, index);

  try {
    const answer = await send(client, on, request.body);

    if (request.from === 'total') return answer.total;

    const n = answer.rows[0]?.n;

    return typeof n === 'number' ? n : 0;
  } catch {
    return undefined;
  }
}

// runPipeline runs the pipeline, then counts the rows of each stage with one
// request for each prefix. A long pipeline is not counted. With decrypt, the
// run asks for plain values; sealed says that some values came back
// encrypted all the same.
export async function runPipeline(client: Ndb, on: string, stages: Stage[], decrypt = false): Promise<AggregateResult> {
  const started = performance.now();
  const answer = await send(client, on, { pipeline: toPipeline(stages), count: true, limit: ROWS }, decrypt);
  const ms = Math.round(performance.now() - started);

  const last = stages.length - 1;
  const counted = stages.length <= COUNT_STAGES_MAX;

  const counts = await Promise.all(
    stages.map((_, index) => {
      if (!counted) return undefined;

      return index === last ? (answer.total ?? answer.rows.length) : countStage(client, on, stages, index);
    }),
  );

  return { rows: answer.rows, total: answer.total, examined: answer.examined, ms, counts, sealed: hasSealed(answer.rows) };
}

// targetOptions lists the collections for the target field: the name, the
// document count and a badge for a stream.
export async function targetOptions(client: Ndb, stats: Stats): Promise<ComboOption[]> {
  const definitions = (await client.collections().catch(() => [])) as (Definition & { stream?: boolean })[];
  const streams = new Set(definitions.filter(definition => definition.stream === true).map(definition => definition.name));

  return stats.collections.map(collection => ({
    id: collection.name,
    hint: `${collection.rows.toLocaleString('en-US')} ${collection.rows === 1 ? 'document' : 'documents'}`,
    badge: streams.has(collection.name) ? 'stream' : undefined,
  }));
}

// matchTarget asks ndb which collections a list or a pattern names (GET
// /{pattern}). A target of one name gives undefined: it needs no count.
export async function matchTarget(client: Ndb, on: string): Promise<TargetMatch | undefined> {
  if (!isMultiTarget(on)) return undefined;

  if (!validTarget(on)) return { text: 'The target must be a collection name, a list with commas, or a pattern.', bad: true };

  try {
    const answer = await client.call<{ collections?: string[] }>('GET', aggregatePath(on).replace(/\/_aggregate$/, ''));

    return { text: matchText(answer.collections?.length ?? 0) };
  } catch (failure) {
    if (failure instanceof NdbError && failure.code === 'unknown_collection') return { text: `No collection has the name ${failure.message}.`, bad: true };

    return { text: failure instanceof Error ? failure.message : 'ndb did not answer.', bad: true };
  }
}

export function aggregateRoutes(app: Hono<Env>, deps: RouteDeps): void {
  // The script of the page asks this while the user types the target.
  app.get('/c/:name/aggregate/match', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.json({ text: 'Not found', bad: true }, 404);

    const match = await matchTarget(deps.client(connection), (c.req.query('on') ?? '').trim());

    return c.json(match ?? { text: '' });
  });

  app.get('/c/:name/aggregate', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);
    const q = c.req.query();

    const view = VIEWS.find(item => item === q.view) ?? 'chart';
    const decrypt = q.decrypt === '1';
    const parsedTarget = target(connection.url);
    const curl = parsedTarget.unix ? { socket: parsedTarget.unix } : { base: parsedTarget.base };

    try {
      const stats = await client.stats();

      const on = (q.on ?? '').trim() || (stats.collections[0]?.name ?? '');
      const text = q.pipeline ?? formatPipeline(DEFAULT_PIPELINE);
      const parsed = parsePipeline(text);

      let result: AggregateResult | undefined;
      let error = parsed.error;

      if (on !== '' && !validTarget(on)) error = 'The target must be a collection name, a list with commas, or a pattern.';

      if (q.run === '1' && on === '') error = 'Write the collection to run the pipeline on.';

      if (q.run === '1' && parsed.stages && !error) {
        try {
          result = await runPipeline(client, on, parsed.stages, decrypt);
        } catch (failure) {
          error = deps.message(failure);
        }
      }

      const [targets, match] = await Promise.all([targetOptions(client, stats), on === '' ? undefined : matchTarget(client, on)]);

      const page = (
        <AggregatePage
          current={deps.currentOf(connection, stats)}
          on={on}
          text={text}
          stages={parsed.stages}
          view={view}
          decrypt={decrypt}
          result={result}
          error={error}
          curl={curl}
          targets={targets}
          match={match}
        />
      );

      return c.html(page);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });
}
