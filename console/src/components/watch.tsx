import type { FC } from 'hono/jsx';
import { useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { JsonTree } from '@/components/json-tree';
import { useEventStream } from '@/hooks/useEventStream';

export interface WatchProps {
  coll: string;
  where: string;
  streamUrl: string;
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
  v?: number;
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

const EventRow: FC<{ event: WatchEvent; open: boolean; onToggle: () => void }> = ({ event, open, onToggle }) => {
  const canOpen = event.doc !== undefined;
  const tone = event.op === 'del' ? 'del' : 'put';

  return (
    <li class="wev">
      <button type="button" class="wev-row" aria-expanded={open} disabled={!canOpen} onClick={onToggle}>
        <span class="tcaret">{canOpen ? (open ? '▾' : '▸') : ''}</span>
        <span class={`opbadge ${tone}`}>{event.op}</span>
        <span class="wev-id mono">{event.id}</span>
        {event.expired && <span class="hint">expired</span>}
        <span class="grow" />
        <span class="hint mono">v{event.v ?? '–'}</span>
        <span class="hint mono">{clock(event.at)}</span>
      </button>

      {open && event.doc && (
        <div class="wev-doc">
          <JsonTree value={event.doc} toolbar={false} />
        </div>
      )}
    </li>
  );
};

export const Watch: FC<WatchProps> = ({ coll, where, streamUrl }) => {
  const [events, setEvents] = useState<WatchEvent[]>([]);
  const [running, setRunning] = useState(true);
  const [from, setFrom] = useState('now');
  const [status, setStatus] = useState('Connecting…');
  const [problem, setProblem] = useState('');
  const [open, setOpen] = useState<Set<number>>(new Set());
  const [seq, setSeq] = useState(0);
  const [session, setSession] = useState(0);

  const params = new URLSearchParams({ coll, from, s: String(session) });

  if (where) params.set('where', where);

  const url = running ? `${streamUrl}?${params}` : undefined;

  useEventStream(url, {
    onMessage: data => {
      const frame = data as Frame;

      if (frame.heartbeat) {
        setStatus('Live · waiting for changes');

        return;
      }

      if (frame.error) {
        setProblem(frame.error.message);

        return;
      }

      if (!frame.op) return;

      setStatus('Live');
      setSeq(current => current + 1);
      setEvents(current => addEvent(current, { key: nextKey(), at: Date.now(), pos: frame.pos, op: frame.op as string, id: frame._id ?? '', v: frame.v, expired: frame._expired, doc: frame.doc }));
    },
    onProblem: message => setProblem(message),
    onClose: () => {
      setRunning(false);
      setStatus('Paused');
    },
  });

  const start = () => {
    setProblem('');
    setStatus('Connecting…');
    setSession(session + 1);
    setRunning(true);
  };

  const toggle = (key: number) => {
    const next = new Set(open);

    if (next.has(key)) next.delete(key);
    else next.add(key);

    setOpen(next);
  };

  return (
    <div class="watch">
      <div class="watch-bar">
        {running ? (
          <button type="button" class="btn small" onClick={() => setRunning(false)}>
            Pause
          </button>
        ) : (
          <button type="button" class="btn primary small" onClick={start}>
            Start
          </button>
        )}

        <button type="button" class="btn small" onClick={() => setEvents([])} disabled={events.length === 0}>
          Clear
        </button>

        <label class="watch-from">
          <span class="hint">Start at</span>
          <select class="input small mono" value={from} disabled={running} onChange={(event: Event) => setFrom((event.target as HTMLSelectElement).value)}>
            <option value="now">now</option>
            <option value="beginning">beginning</option>
          </select>
        </label>

        <span class="grow" />

        <span class="hint" role="status">
          {running ? status : 'Paused'} · {events.length} kept (max {MAX_EVENTS}) · {seq} received
        </span>
      </div>

      {problem && <p class="note bad">{problem}</p>}

      {events.length === 0 && !problem && <p class="hint">No changes yet. Insert or update a document in {coll} and it shows here.</p>}

      <ul class="wlist">
        {events.map(event => (
          <EventRow event={event} open={open.has(event.key)} onToggle={() => toggle(event.key)} />
        ))}
      </ul>
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

  render(<Watch {...props} />, host);
}

if (typeof document !== 'undefined') mount();
