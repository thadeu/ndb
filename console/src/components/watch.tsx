import type { FC } from 'hono/jsx';
import { useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { JsonTree } from '@/components/json-tree';
import { agoText, FROM_OPTIONS, preview, readFrom, sameSettings, settingsQuery, watchSnippet, type WatchSettings } from '@/components/watch-model';
import { useEventStream } from '@/hooks/useEventStream';
import { useNow } from '@/hooks/useNow';
import { noSpelling } from '@/ui/input';

export interface WatchCollection {
  name: string;
  direct: boolean;
}

export interface WatchProps {
  action: string;
  streamUrl: string;
  explorerUrl: string;
  streamsHref: string;
  collections: WatchCollection[];
  settings: WatchSettings;
  live?: boolean;
}

export interface WatchEvent {
  key: number;
  at: number;
  pos?: string;
  op: string;
  id: string;
  v?: number;
  expired?: boolean;
  doc?: Record<string, unknown>;
}

export const MAX_EVENTS = 500;

let counter = 0;

const nextKey = () => ++counter;

interface Frame {
  pos?: string;
  op?: string;
  _id?: string;
  _v?: number;
  _expired?: boolean;
  doc?: Record<string, unknown>;
  heartbeat?: boolean;
  error?: { code: string; message: string };
}

// addEvent puts a new event first and keeps at most MAX_EVENTS.
export function addEvent(events: WatchEvent[], next: WatchEvent): WatchEvent[] {
  return [next, ...events].slice(0, MAX_EVENTS);
}

const clock = (at: number) => new Date(at).toLocaleTimeString('en-GB', { hour12: false });

const opTone = (op: string) => (op === 'put' ? 'put' : op === 'del' ? 'del' : 'other');

const EventRow: FC<{ event: WatchEvent; open: boolean; href: string; onToggle: () => void }> = ({ event, open, href, onToggle }) => {
  const canOpen = event.doc !== undefined;
  const caret = canOpen ? (open ? '▾' : '▸') : '';

  return (
    <li class="wev">
      <div class="wev-line mono">
        <button
          type="button"
          class="tcaret wev-caret"
          aria-expanded={open}
          aria-label="Show the document"
          disabled={!canOpen}
          onClick={onToggle}
        >
          {caret}
        </button>

        <span class="wev-dim">{clock(event.at)}</span>

        <span class={`opbadge ${opTone(event.op)}`}>{event.op}</span>

        {event.id !== '' && (
          <a class="wev-id" href={href}>
            {event.id}
          </a>
        )}

        {event.expired && <span class="wev-dim">expired</span>}

        {event.v !== undefined && <span class="wev-dim">_v {event.v}</span>}

        <span class="wev-preview">{preview(event.doc)}</span>
      </div>

      {open && event.doc && (
        <div class="wev-doc">
          <JsonTree value={event.doc} toolbar={false} />
        </div>
      )}
    </li>
  );
};

const Snippet: FC<{ text: string }> = ({ text }) => {
  const [first = '', ...rest] = text.split('\n');
  const [method = '', path = ''] = first.split(' ');

  return (
    <pre class="snippet mono">
      <span class="tok-method">{method}</span> {path}
      {`\n${rest.join('\n')}`}
    </pre>
  );
};

export const Watch: FC<WatchProps> = ({ action, streamUrl, explorerUrl, streamsHref, collections, settings, live = true }) => {
  const [draft, setDraft] = useState<WatchSettings>(settings);
  const [active, setActive] = useState<WatchSettings>(settings);
  const [running, setRunning] = useState(live);
  const [session, setSession] = useState(0);

  const [events, setEvents] = useState<WatchEvent[]>([]);
  const [seq, setSeq] = useState(0);
  const [frozen, setFrozen] = useState<{ events: WatchEvent[]; seq: number } | null>(null);
  const [open, setOpen] = useState<Set<number>>(new Set());

  const [connected, setConnected] = useState(false);
  const [beatAt, setBeatAt] = useState<number | null>(null);
  const [problem, setProblem] = useState('');

  const now = useNow(running);

  const direct = collections.find(item => item.name === active.coll)?.direct === true;
  const url = running && !direct ? `${streamUrl}?${settingsQuery(active)}&s=${session}` : undefined;

  useEventStream(url, {
    onReady: () => setConnected(true),
    onMessage: data => {
      const frame = data as Frame;

      if (frame.heartbeat) {
        setBeatAt(Date.now());

        return;
      }

      if (frame.error) {
        setProblem(frame.error.message);

        return;
      }

      if (!frame.op) return;

      setSeq(current => current + 1);
      setEvents(current => addEvent(current, { key: nextKey(), at: Date.now(), pos: frame.pos, op: frame.op as string, id: frame._id ?? '', v: frame._v, expired: frame._expired, doc: frame.doc }));
    },
    onProblem: message => setProblem(message),
    onClose: () => {
      setRunning(false);
      setConnected(false);
    },
  });

  const start = (event?: Event) => {
    event?.preventDefault();

    setProblem('');
    setConnected(false);
    setBeatAt(null);

    setActive(draft);
    setSession(session + 1);
    setRunning(true);

    window.history.replaceState(null, '', `${action}?${settingsQuery(draft)}`);
  };

  const stop = () => {
    setRunning(false);
    setConnected(false);
  };

  const change = (patch: Partial<WatchSettings>) => setDraft({ ...draft, ...patch });

  const changeConsumer = (consumer: string) => {
    const from = consumer !== '' ? 'consumer' : draft.from === 'consumer' ? 'now' : draft.from;

    change({ consumer, from });
  };

  const toggle = (key: number) => {
    const next = new Set(open);

    if (next.has(key)) next.delete(key);
    else next.add(key);

    setOpen(next);
  };

  const clear = () => {
    setEvents([]);
    setSeq(0);

    if (frozen) setFrozen({ events: [], seq: 0 });
  };

  const shown = frozen ? frozen.events : events;
  const waiting = frozen ? seq - frozen.seq : 0;
  const dirty = !sameSettings(draft, active);
  const on = running && connected;

  const beat = beatAt === null ? 'no heartbeat yet' : `last heartbeat ${agoText(beatAt, now)}`;
  const state = !live ? 'needs JavaScript' : direct ? 'no watch' : !running ? 'stopped' : connected ? beat : 'connecting…';
  const count = `${seq} ${seq === 1 ? 'event' : 'events'}`;

  const docHref = (id: string) => `${explorerUrl}?${new URLSearchParams({ coll: active.coll, tab: 'documents', id })}`;

  return (
    <div class="watch">
      <form
        class="watch-settings"
        method="get"
        action={action}
        aria-label="Watch settings"
        onSubmit={start}
      >
        <label class="watch-field">
          <span class="label">Collection</span>

          <select
            class="input mono"
            name="coll"
            onChange={(event: Event) => change({ coll: (event.target as HTMLSelectElement).value })}
          >
            {collections.map(item => (
              <option value={item.name} selected={item.name === draft.coll}>
                {item.name}
              </option>
            ))}
          </select>
        </label>

        <label class="watch-field wide">
          <span class="label">Filter</span>

          <input
            class="input mono"
            name="where"
            value={draft.where}
            placeholder='{"status":"paid"}'
            {...noSpelling}
            onInput={(event: Event) => change({ where: (event.target as HTMLInputElement).value })}
          />
        </label>

        <label class="watch-field">
          <span class="label">Start from</span>

          <select
            class="input"
            name="from"
            onChange={(event: Event) => change({ from: readFrom((event.target as HTMLSelectElement).value) })}
          >
            {FROM_OPTIONS.map(item => (
              <option value={item.id} selected={item.id === draft.from}>
                {item.label}
              </option>
            ))}
          </select>
        </label>

        <label class="watch-field">
          <span class="label">Consumer (optional)</span>

          <input
            class="input mono"
            name="consumer"
            value={draft.consumer}
            placeholder="no ack: read only"
            {...noSpelling}
            onInput={(event: Event) => changeConsumer((event.target as HTMLInputElement).value.trim())}
          />
        </label>

        {running && !dirty ? (
          <button type="button" class="btn danger watch-go" onClick={stop}>
            Stop
          </button>
        ) : (
          <button type="submit" class="btn primary watch-go">
            {running ? 'Restart' : 'Start'}
          </button>
        )}
      </form>

      {problem && <p class="note bad">{problem}</p>}

      {direct && <p class="note bad">Direct collections have no watch: ndb reads the files of another app, so there is no change feed.</p>}

      <div class="watch-cols">
        <section class="card flush watch-live" aria-label="Live">
          <div class="watch-live-head">
            <span class={on ? 'dot on' : 'dot'} aria-hidden="true" />

            <h2 class="card-title">Live</h2>

            <span class="hint" role="status">
              {count} · {state}
            </span>

            <span class="grow" />

            <button
              type="button"
              class="btn small"
              aria-pressed={frozen !== null}
              disabled={!live}
              onClick={() => setFrozen(frozen ? null : { events, seq })}
            >
              {frozen ? `Resume scroll (${waiting} new)` : 'Pause scroll'}
            </button>

            <button
              type="button"
              class="btn small"
              disabled={shown.length === 0}
              onClick={clear}
            >
              Clear
            </button>
          </div>

          {!live && <p class="watch-empty hint">The live tail needs JavaScript. The settings are kept in the URL.</p>}

          {live && shown.length === 0 && <p class="watch-empty hint">No changes yet. Insert or update a document in {active.coll} and it shows here. The list keeps the last {MAX_EVENTS} events.</p>}

          <ul class="wlist">
            {shown.map(event => (
              <EventRow
                event={event}
                open={open.has(event.key)}
                href={docHref(event.id)}
                onToggle={() => toggle(event.key)}
              />
            ))}
          </ul>
        </section>

        <aside class="watch-side">
          <section class="card soft">
            <h2 class="card-title">Use this watch in code</h2>

            <Snippet text={watchSnippet(draft)} />

            <p class="hint">
              An app sends its own consumer name, then acks each event with <span class="mono">POST /{draft.coll}/_ack</span>. The console never acks: with no consumer it opens the watch as <span class="mono">ndb-ui-xxxxxxxx</span>, and ndb keeps a position row for each name.
            </p>

            <a class="watch-more" href={streamsHref}>
              Need groups, retries and a dead letter? Use a stream.
            </a>
          </section>
        </aside>
      </div>
    </div>
  );
};

// mount replaces the server fallback with the live tail.
export function mount(): void {
  const host = document.getElementById('watch');

  if (!host || host.dataset.mounted) return;

  host.dataset.mounted = 'true';

  const props = JSON.parse(host.dataset.props ?? '{}') as WatchProps;

  host.replaceChildren();

  render(<Watch {...props} live />, host);
}

if (typeof document !== 'undefined') mount();
