import type { FC } from 'hono/jsx';
import { useRef, useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { type Block, blockAt, defaultText, formatBlock, lineAt, parseBlocks, tokenize } from '@/components/devtools-blocks';
import { buildCurl } from '@/components/curl';
import { editKey } from '@/components/editor-keys';
import { JsonTree } from '@/components/json-tree';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import { useOutsideClick } from '@/hooks/useOutsideClick';
import { useSend } from '@/hooks/useSend';
import { Segmented } from '@/ui/segmented';
import { noSpelling } from '@/ui/input';

export interface DevtoolsProps {
  name: string;
  collection: string;
  sendUrl: string;
  browserHref: string;
  curl: { kind: 'unix' | 'tcp'; socket?: string; base?: string };
}

interface Answer {
  status: number;
  ms: number;
  bytes: number;
  headers: Record<string, string>;
  body: string;
  truncated?: boolean;
}

interface Failure {
  message: string;
}

interface Entry {
  method: string;
  path: string;
  body: string;
  status: number;
  at: number;
}

type View = 'tree' | 'raw' | 'headers';

const VIEWS = [
  { id: 'tree', label: 'Tree' },
  { id: 'raw', label: 'Raw' },
  { id: 'headers', label: 'Headers' },
];

const HISTORY_LIMIT = 50;
const LINE_HEIGHT = 22;

function isNdjson(answer: Answer): boolean {
  return (answer.headers['content-type'] ?? '').includes('ndjson');
}

// treeValue turns the answer into a value for the tree: a JSON body as it is,
// an NDJSON body as one node per line, and undefined for other text.
export function treeValue(answer: Answer): unknown {
  if (isNdjson(answer)) {
    const lines = answer.body.split('\n').filter(line => line.trim() !== '');

    return Object.fromEntries(lines.map((line, index) => [`line ${index + 1}`, safeParse(line)]));
  }

  return safeParse(answer.body, undefined);
}

function safeParse(text: string, fallback: unknown = text): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return fallback;
  }
}

function statusTone(status: number): string {
  if (status >= 500) return 'bad';

  if (status >= 400) return 'warn';

  return 'ok';
}

function describe(answer: Answer): string {
  const rows = isNdjson(answer) ? ` · ${answer.body.split('\n').filter(line => line.trim() !== '').length} rows` : '';

  return `${answer.ms} ms${rows} · ${answer.bytes.toLocaleString('en-US')} B`;
}

const Gutter: FC<{ count: number; block?: Block; runs: Set<number> }> = ({ count, block, runs }) => (
  <div class="ed-gutter" aria-hidden="true">
    {Array.from({ length: count }, (_, index) => {
      const inside = block !== undefined && index >= block.start && index <= block.end;

      return (
        <div class={inside ? 'ed-ln on' : 'ed-ln'}>
          <span class="ed-run">{runs.has(index) ? '▶' : ''}</span>
          <span>{index + 1}</span>
        </div>
      );
    })}
  </div>
);

const Highlight: FC<{ lines: string[]; block?: Block }> = ({ lines, block }) => (
  <pre class="ed-hl" aria-hidden="true">
    {lines.map((line, index) => {
      const inside = block !== undefined && index >= block.start && index <= block.end;

      return (
        <div class={inside ? 'ed-line on' : 'ed-line'}>
          {line === '' ? '​' : tokenize(line).map(token => <span class={`tok-${token.tone}`}>{token.text}</span>)}
        </div>
      );
    })}
  </pre>
);

const FormatButton: FC<{ line: number; onClick: () => void }> = ({ line, onClick }) => (
  <button
    type="button"
    class="ed-fmt"
    style={`top:${line * 22}px`}
    title="Beautify JSON"
    aria-label="Beautify JSON"
    onClick={onClick}
  >
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
      <path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1" />
      <path d="M16 3h1a2 2 0 0 1 2 2v5a2 2 0 0 0 2 2 2 2 0 0 0-2 2v5a2 2 0 0 1-2 2h-1" />
    </svg>
  </button>
);

const Chips: FC<{ headers: Record<string, string> }> = ({ headers }) => (
  <div class="chips">
    {Object.entries(headers).map(([key, value]) => (
      <span class="chip mono">
        <span class="chip-key">{key}</span> {value}
      </span>
    ))}
  </div>
);

const ResponsePane: FC<{ answer: Answer | null; failure: Failure | null; view: View; onView: (id: string) => void; browserHref: string }> = ({ answer, failure, view, onView, browserHref }) => {
  if (failure) return <p class="note bad">{failure.message}</p>;

  if (!answer) return <p class="hint">Send a request to see the answer here.</p>;

  const value = treeValue(answer);
  const treeable = value !== undefined && typeof value === 'object' && value !== null;
  const shown: View = view === 'tree' && !treeable ? 'raw' : view;

  return (
    <>
      <div class="resp-head">
        <span class={`status ${statusTone(answer.status)}`}>{answer.status}</span>
        <span class="hint">{describe(answer)}</span>

        <span class="grow" />

        <Segmented items={VIEWS} value={shown} label="Response view" onSelect={onView} />
      </div>

      <Chips headers={answer.headers} />

      {answer.truncated && <p class="note bad">The answer is cut at 4 MiB. Add a limit to the request.</p>}

      {shown === 'tree' && <JsonTree value={value} emphasizeTop={isNdjson(answer)} />}

      {shown === 'raw' && <pre class="raw mono">{answer.body}</pre>}

      {shown === 'headers' && (
        <div class="scroll">
          <table aria-label="Response headers">
            <tbody>
              {Object.entries(answer.headers).map(([key, item]) => (
                <tr>
                  <td class="mono strong">{key}</td>
                  <td class="mono">{item}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <span class="hint">
        NDJSON answers show one node per line. Open a row in <a href={browserHref}>Explorer</a> to edit it.
      </span>
    </>
  );
};

const HistoryList: FC<{ entries: Entry[]; onPick: (entry: Entry) => void }> = ({ entries, onPick }) => (
  <div class="popover" role="menu">
    {entries.length === 0 && <p class="hint">No requests yet.</p>}

    {entries.map(entry => (
      <button type="button" class="history-item" role="menuitem" onClick={() => onPick(entry)}>
        <span class={`status small ${statusTone(entry.status)}`}>{entry.status}</span>
        <span class="mono history-path">
          {entry.method} {entry.path}
        </span>
        <span class="hint">{new Date(entry.at).toLocaleTimeString('en-GB')}</span>
      </button>
    ))}
  </div>
);

export const Devtools: FC<DevtoolsProps> = ({ name, collection, sendUrl, browserHref, curl }) => {
  const [text, setText] = useLocalStorage(`ndb-console:${name}:devtools:text`, defaultText(collection));
  const [history, setHistory] = useLocalStorage<Entry[]>(`ndb-console:${name}:devtools:history`, []);
  const [line, setLine] = useState(0);
  const [answer, setAnswer] = useState<Answer | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [view, setView] = useState<View>('tree');
  const [running, setRunning] = useState(false);
  const [menu, setMenu] = useState(false);
  const [toast, setToast] = useState('');

  const area = useRef<HTMLTextAreaElement | null>(null);
  const popover = useRef<HTMLDivElement | null>(null);

  const send = useSend(sendUrl);

  useOutsideClick(popover, menu, () => setMenu(false));

  const lines = text.split('\n');
  const blocks = parseBlocks(text);
  const block = blockAt(blocks, line);
  const runs = new Set(blocks.map(item => item.start));

  const editorHeight = `${lines.length * LINE_HEIGHT + 28}px`;

  const say = (message: string) => {
    setToast(message);
    setTimeout(() => setToast(''), 2500);
  };

  const track = () => {
    const element = area.current;

    if (element) setLine(lineAt(element.value, element.selectionStart));
  };

  const run = async () => {
    if (!block || running) {
      if (!block) say('Write a line like GET /_stats first.');

      return;
    }

    setRunning(true);
    setFailure(null);

    const result = await send<Answer & { message?: string }>({ method: block.method, path: block.path, body: block.body });

    setRunning(false);

    if (!result.ok && result.body.status === undefined) {
      setAnswer(null);
      setFailure({ message: result.body.message ?? 'The request failed.' });

      return;
    }

    setAnswer(result.body);

    const entry: Entry = { method: block.method, path: block.path, body: block.body, status: result.body.status, at: Date.now() };

    setHistory([entry, ...history.filter(item => !(item.method === entry.method && item.path === entry.path && item.body === entry.body))].slice(0, HISTORY_LIMIT));
  };

  const keys = (event: KeyboardEvent) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault();
      run();

      return;
    }

    const element = area.current;

    if (!element || event.metaKey || event.ctrlKey || event.altKey || event.isComposing) return;

    const edit = editKey(element.value, element.selectionStart, element.selectionEnd, { key: event.key, shift: event.shiftKey });

    if (!edit) return;

    event.preventDefault();

    element.value = edit.text;
    element.setSelectionRange(edit.start, edit.end);

    setText(edit.text);
    setLine(lineAt(edit.text, edit.start));
  };

  const format = () => {
    const formatted = block ? formatBlock(text, block) : undefined;

    if (formatted === undefined) {
      say('This block has no JSON body to format.');

      return;
    }

    setText(formatted);
  };

  const copyCurl = async () => {
    if (!block) {
      say('Put the cursor in a request first.');

      return;
    }

    const copied = await copyText(buildCurl({ method: block.method, path: block.path, body: block.body, socket: curl.socket, base: curl.base }));

    say(copied ? 'Copied the curl command.' : 'The browser blocked the clipboard.');
  };

  const load = (entry: Entry) => {
    const added = `${entry.method} ${entry.path}${entry.body ? `\n${entry.body}` : ''}`;
    const next = `${text.replace(/\s+$/, '')}\n\n${added}\n`;

    setText(next);
    setLine(next.split('\n').length - 2);
    setMenu(false);
  };

  return (
    <div class="dt">
      <div class="dt-bar">
        <button type="button" class="btn primary small" disabled={running} onClick={run}>
          {running ? 'Sending…' : '▶ Send'}
        </button>

        <span class="hint dt-hint">Ctrl or Cmd + Enter sends the request under the cursor</span>

        <span class="grow" />

        {toast && <span class="hint" role="status">{toast}</span>}

        <div class="pop-host" ref={popover}>
          <button type="button" class="btn small" aria-expanded={menu} onClick={() => setMenu(!menu)}>
            History
          </button>

          {menu && <HistoryList entries={history} onPick={load} />}
        </div>

        <button type="button" class="btn small" onClick={copyCurl}>
          Copy as curl
        </button>
        <button type="button" class="btn small" onClick={format}>
          Format
        </button>
      </div>

      <div class="dt-panes">
        <section class="ed" aria-label="Requests">
          <div class="ed-scroll">
            <Gutter count={lines.length} block={block} runs={runs} />

            <div class="ed-main">
              <Highlight lines={lines} block={block} />

              <textarea
                ref={area}
                class="ed-ta"
                style={`height:${editorHeight}`}
                value={text}
                {...noSpelling}
                wrap="off"
                aria-label="Requests"
                onInput={(event: Event) => {
                  setText((event.target as HTMLTextAreaElement).value);
                  track();
                }}
                onKeyDown={keys}
                onKeyUp={track}
                onClick={track}
                onFocus={track}
              />

              {block?.body ? <FormatButton line={block.start} onClick={format} /> : null}
            </div>
          </div>
        </section>

        <section class="resp" aria-label="Response">
          <ResponsePane answer={answer} failure={failure} view={view} onView={id => setView(id as View)} browserHref={browserHref} />
        </section>
      </div>
    </div>
  );
};

// mount replaces the server fallback with the editor.
export function mount(): void {
  const host = document.getElementById('devtools');

  if (!host || host.dataset.mounted) return;

  host.dataset.mounted = 'true';

  const props = JSON.parse(host.dataset.props ?? '{}') as DevtoolsProps;

  host.replaceChildren();

  render(<Devtools {...props} />, host);
}

if (typeof document !== 'undefined') mount();
