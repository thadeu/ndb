import type { FC } from 'hono/jsx';

import { type Stage, STAGES, aggregatePath, cellText, chartOf, columnsOf, formatBody, formatPipeline, moveStage, requestBody, whatOf } from '@/aggregate';
import { buildCurl } from '@/components/curl';
import { Layout, type Current } from '@/html/layout';
import type { Doc } from '@/ndb';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { CodeEditor } from '@/ui/code-editor';
import { Combobox, type ComboOption } from '@/ui/combobox';
import { Dialog, openDialog } from '@/ui/dialog';
import { Empty } from '@/ui/empty';
import { Field } from '@/ui/field';
import { Icon } from '@/ui/icon';
import { Input } from '@/ui/input';
import { SegmentedNav } from '@/ui/segmented-nav';
import { Table } from '@/ui/table';

export type AggregateView = 'chart' | 'table' | 'json';

// AggregateResult is the answer of one run. counts has the rows that leave
// each stage, or undefined for a stage that was not counted.
export interface AggregateResult {
  rows: Doc[];
  total?: number;
  examined?: number;
  ms: number;
  counts: (number | undefined)[];
}

export interface AggregatePageProps {
  current: Current;
  on: string;
  text: string;
  stages?: Stage[];
  view: AggregateView;
  result?: AggregateResult;
  error?: string;
  curl: { socket?: string; base?: string };
  // targets are the collections of the daemon, for the list of the target
  // field. match says how many collections a list or a pattern names.
  targets?: ComboOption[];
  match?: TargetMatch;
}

export interface TargetMatch {
  text: string;
  bad?: boolean;
}

export interface AggregateQuery {
  on: string;
  pipeline: string;
  view?: AggregateView;
  run?: boolean;
}

export function aggregateHref(name: string, query: AggregateQuery): string {
  const params = new URLSearchParams({ on: query.on, pipeline: query.pipeline });

  if (query.view && query.view !== 'chart') params.set('view', query.view);
  if (query.run) params.set('run', '1');

  return `c/${encodeURIComponent(name)}/aggregate?${params}`;
}

const rowsLabel = (count: number) => `${count.toLocaleString('en-US')} ${count === 1 ? 'row' : 'rows'}`;

interface StageCardProps {
  number: number;
  stage: Stage;
  out?: number;
  removeHref: string;
  upHref?: string;
  downHref?: string;
}

interface MoveButtonProps {
  to: 'up' | 'down';
  href?: string;
  fallback: string;
}

// MoveButton is a link, so it works without the script. At an end of the
// pipeline it has no target and looks disabled.
const MoveButton: FC<MoveButtonProps> = ({ to, href, fallback }) => (
  <Button
    variant="ghost"
    size="sm"
    class="icon-only agg-move"
    href={href ?? fallback}
    aria-label={to === 'up' ? 'Move up' : 'Move down'}
    title={to === 'up' ? 'Move up' : 'Move down'}
    aria-disabled={href ? undefined : 'true'}
    data-agg-move={to}
  >
    <Icon name={to === 'up' ? 'arrow-up' : 'arrow-down'} size={16} />
  </Button>
);

const StageCard: FC<StageCardProps> = ({ number, stage, out, removeHref, upHref, downHref }) => (
  <article class="agg-stage" data-agg-stage data-op={stage.op}>
    <div class="agg-stage-head">
      <span class="agg-num" data-agg-num>
        {number}
      </span>

      <strong class="mono agg-op" data-agg-op>
        {stage.op}
      </strong>

      <span class="hint agg-what" data-agg-what>
        {whatOf(stage.op)}
      </span>

      <span class="agg-out" data-agg-out>
        {out !== undefined && <Badge>{rowsLabel(out)}</Badge>}
      </span>

      <div class="agg-stage-tools">
        <MoveButton to="up" href={upHref} fallback={removeHref} />
        <MoveButton to="down" href={downHref} fallback={removeHref} />

        <Button
          variant="ghost"
          size="sm"
          class="icon-only"
          href={removeHref}
          aria-label="Remove this stage"
          title="Remove this stage"
          data-agg-remove
        >
          <Icon name="x" size={16} />
        </Button>
      </div>
    </div>

    <CodeEditor
      value={formatBody(stage.body)}
      label={`Body of the stage ${stage.op}`}
      readonly
      data-agg-body
    />
  </article>
);

const Bars: FC<{ rows: Doc[] }> = ({ rows }) => {
  const chart = chartOf(rows);

  if (!chart) return <p class="hint">A chart needs a group result with one number for each group. Use Table or JSON for this result.</p>;

  return (
    <div class="agg-chart">
      {chart.bars.map(bar => (
        <div class="agg-bar-row">
          <span class="mono agg-bar-label" title={bar.label}>
            {bar.label}
          </span>

          <span class="agg-bar-track">
            <span class="agg-bar-fill" style={`width:${bar.width}%`} />
          </span>

          <span class="num agg-bar-value">{bar.value.toLocaleString('en-US')}</span>
        </div>
      ))}

      <span class="stat-sub">
        <span class="mono">{chart.field}</span> for each <span class="mono">_id</span>
      </span>
    </div>
  );
};

const RowsTable: FC<{ rows: Doc[] }> = ({ rows }) => {
  const columns = columnsOf(rows);

  return (
    <Table minWidth={Math.min(columns.length * 140, 900)} label="Result rows">
      <thead>
        <tr>
          {columns.map(column => (
            <th>{column}</th>
          ))}
        </tr>
      </thead>

      <tbody>
        {rows.map(row => (
          <tr>
            {columns.map(column => (
              <td class={typeof row[column] === 'number' ? 'num' : 'mono'}>{cellText(row[column])}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </Table>
  );
};

interface ResultCardProps {
  result?: AggregateResult;
  error?: string;
  view: AggregateView;
  hrefOf: (view: AggregateView) => string;
}

const ResultCard: FC<ResultCardProps> = ({ result, error, view, hrefOf }) => {
  const views = (['chart', 'table', 'json'] as const).map(id => ({ id, label: id === 'json' ? 'JSON' : id === 'chart' ? 'Chart' : 'Table', href: hrefOf(id) }));
  const count = result ? (result.total ?? result.rows.length) : 0;
  const facts = result ? [`${count.toLocaleString('en-US')} ${count === 1 ? 'group' : 'groups'}`, result.examined === undefined ? '' : `examined ${result.examined.toLocaleString('en-US')}`, `${result.ms} ms`].filter(Boolean).join(' · ') : '';

  return (
    <Card class="agg-result">
      <div class="agg-result-head">
        <h2 class="card-title">Result</h2>

        {result && <Badge tone="warn">{facts}</Badge>}

        <span class="agg-gap" />

        {result && <SegmentedNav items={views} value={view} label="View" />}
      </div>

      {error && <p class="note bad">{error}</p>}

      {!result && !error && <Empty title="No result yet" description="Select Run to send the pipeline to ndb." />}

      {result && result.rows.length === 0 && <Empty title="No rows" description="The pipeline ran and gave no row." />}

      {result && result.rows.length > 0 && view === 'chart' && <Bars rows={result.rows} />}

      {result && result.rows.length > 0 && view === 'table' && <RowsTable rows={result.rows} />}

      {result && result.rows.length > 0 && view === 'json' && <CodeBlock code={JSON.stringify(result.rows, null, 2)} />}

      {result && count > result.rows.length && <p class="hint">The page shows the first {result.rows.length} rows of {count.toLocaleString('en-US')}.</p>}
    </Card>
  );
};

export const AggregatePage: FC<AggregatePageProps> = ({ current, on, text, stages, view, result, error, curl, targets = [], match }) => {
  const action = `c/${encodeURIComponent(current.name)}/aggregate`;
  const list = stages ?? [];

  const hrefOf = (next: Stage[], run = false) => aggregateHref(current.name, { on, pipeline: formatPipeline(next), view, run });
  const viewHref = (next: AggregateView) => aggregateHref(current.name, { on, pipeline: text, view: next, run: true });

  const path = aggregatePath(on);
  const body = stages ? requestBody(stages) : text;
  const curlLine = buildCurl({ method: 'POST', path, body, socket: curl.socket, base: curl.base });

  return (
    <Layout title="Aggregate" current={current} tab="aggregate">
      <form
        class="agg"
        method="get"
        action={action}
        data-agg
        data-agg-name={current.name}
        data-agg-broken={stages ? undefined : true}
        data-socket={curl.socket}
        data-base={curl.base}
      >
        <header class="page-head">
          <div class="page-head-text">
            <h1>Aggregate</h1>
            <p class="lead">Build a pipeline stage by stage. Each stage shows how many rows leave it.</p>
          </div>

          <div class="agg-actions">
            <div class="agg-target">
              <label class="agg-on">
                <span class="hint">on</span>

                <Combobox
                  id="agg-on"
                  mono
                  multiple
                  class="agg-on-input"
                  name="on"
                  value={on}
                  options={targets}
                  required
                  aria-label="Collection, list or pattern"
                  data-agg-on
                />
              </label>

              <span
                class={match?.bad ? 'hint bad agg-match' : 'hint agg-match'}
                aria-live="polite"
                hidden={!match}
                data-agg-match
              >
                {match?.text}
              </span>
            </div>

            <Button type="button" hidden data-agg-saved-open {...openDialog('agg-saved')}>
              Saved pipelines
            </Button>

            <Button variant="dark" type="submit">
              Run
            </Button>
          </div>
        </header>

        <p class="hint">
          The target is one collection, a list (<code class="mono">orders-2026-11,orders-2026-10</code>) or a pattern (<code class="mono">orders-2026-*</code>).
        </p>

        <input type="hidden" name="view" value={view} />
        <input type="hidden" name="run" value="1" />

        <div class="agg-cols">
          <section class="agg-col" aria-label="Pipeline">
            <div class="agg-stages" data-agg-stages>
              {list.map((stage, index) => (
                <StageCard
                  number={index + 1}
                  stage={stage}
                  out={result?.counts[index]}
                  removeHref={hrefOf(list.filter((_, at) => at !== index))}
                  upHref={index > 0 ? hrefOf(moveStage(list, index, -1)) : undefined}
                  downHref={index < list.length - 1 ? hrefOf(moveStage(list, index, 1)) : undefined}
                />
              ))}
            </div>

            <p class="note bad" hidden data-agg-error />

            <div class="agg-add">
              <span class="label">Add stage</span>

              {STAGES.map(info => (
                <Button
                  size="sm"
                  class="agg-chip mono"
                  href={hrefOf([...list, { op: info.op, body: info.sample }])}
                  title={info.what}
                  data-agg-add
                  data-op={info.op}
                  data-what={info.what}
                  data-sample={formatBody(info.sample)}
                >
                  {info.op}
                </Button>
              ))}
            </div>

            <div class="agg-raw" data-agg-rawbox>
              <Field id="agg-pipeline" label="Pipeline as JSON">
                <CodeEditor
                  id="agg-pipeline"
                  name="pipeline"
                  value={text}
                  label="Pipeline as JSON"
                  data-agg-raw
                />
              </Field>
            </div>
          </section>

          <section class="agg-col" aria-label="Result">
            <ResultCard result={result} error={error} view={view} hrefOf={viewHref} />

            <Card class="agg-request">
              <div class="agg-request-head">
                <h2 class="card-title">Request</h2>

                <div class="agg-request-actions">
                  <Button size="sm" type="button" data-copy={curlLine} data-agg-curl>
                    Copy as curl
                  </Button>

                  <Button size="sm" href={`c/${encodeURIComponent(current.name)}/devtools`}>
                    Open in Dev Tools
                  </Button>
                </div>
              </div>

              <pre class="agg-request-pre">
                <span class="agg-method">POST</span> <span data-agg-path>{path}</span>
                {'\n'}
                <span data-agg-json>{body}</span>
              </pre>
            </Card>
          </section>
        </div>
      </form>

      <template data-agg-template>
        <StageCard number={0} stage={{ op: '', body: {} }} removeHref={action} />
      </template>

      <Dialog id="agg-saved" title="Saved pipelines" description="Kept in this browser only." closeHref={aggregateHref(current.name, { on, pipeline: text, view })}>
        <div class="stack">
          <ul class="agg-saved-list" data-agg-saved-list />

          <p class="hint" data-agg-saved-empty>
            No saved pipeline yet.
          </p>

          <Field id="agg-saved-name" label="Save this pipeline as">
            <Input id="agg-saved-name" placeholder="sold per sku" data-agg-saved-name />
          </Field>

          <div class="form-actions">
            <Button variant="primary" type="button" data-agg-saved-save>
              Save
            </Button>
          </div>
        </div>
      </Dialog>
    </Layout>
  );
};
