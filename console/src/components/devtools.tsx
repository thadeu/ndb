import type { FC } from 'hono/jsx';
import { useRef, useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { type Block, blockAt, defaultText, formatBlock, isComment, lineAt, parseBlocks, tokenize } from '@/components/devtools-blocks';
import { type Example, type ExampleGroup, exampleGroups, findExamples, placeExample } from '@/components/devtools-examples';
import { buildCurl } from '@/components/curl';
import { editKey, toggleComment } from '@/components/editor-keys';
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

type View = 'body' | 'headers';

const VIEWS = [
  { id: 'body', label: 'Body' },
  { id: 'headers', label: 'Headers' },
];

const STATUS_TEXT: Record<number, string> = {
  200: 'OK',
  201: 'Created',
  204: 'No Content',
  400: 'Bad Request',
  401: 'Unauthorized',
  403: 'Forbidden',
  404: 'Not Found',
  405: 'Method Not Allowed',
  409: 'Conflict',
  410: 'Gone',
  413: 'Content Too Large',
  423: 'Locked',
  429: 'Too Many Requests',
  500: 'Internal Server Error',
  503: 'Service Unavailable',
};

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

// metaOf picks the headers that the response card shows under the body: the
// request id first (the log lines of the daemon have it as request_id), then
// the X-Ndb-* headers of a read (plan, total, examined, next).
export function metaOf(headers: Record<string, string>): { label: string; value: string }[] {
  const name = (key: string) => key.split('-').map(part => part.charAt(0).toUpperCase() + part.slice(1)).join('-');
  const keys = Object.keys(headers).filter(key => key.startsWith('x-ndb-'));

  return ['x-request-id', ...keys].filter(key => headers[key] !== undefined).map(key => ({ label: name(key), value: headers[key] as string }));
}

const ResponsePane: FC<{ answer: Answer | null; failure: Failure | null; view: View; raw: boolean; onView: (id: string) => void; onRaw: () => void; browserHref: string }> = ({ answer, failure, view, raw, onView, onRaw, browserHref }) => {
  if (failure) return <p class="note bad">{failure.message}</p>;

  if (!answer) return <p class="hint">Send a request to see the answer here.</p>;

  const value = treeValue(answer);
  const treeable = value !== undefined && typeof value === 'object' && value !== null;
  const asTree = treeable && !raw;
  const label = `${answer.status} ${STATUS_TEXT[answer.status] ?? ''}`.trim();
  const meta = metaOf(answer.headers);

  return (
    <>
      <div class="resp-head">
        <span class={`status ${statusTone(answer.status)}`}>{label}</span>
        <span class="hint">{describe(answer)}</span>

        <span class="grow" />

        <Segmented
          items={VIEWS}
          value={view}
          label="View"
          onSelect={onView}
        />
      </div>

      {answer.truncated && <p class="note bad">The answer is cut at 4 MiB. Add a limit to the request.</p>}

      {view === 'body' && treeable && (
        <div class="resp-sub">
          <button type="button" class="tbtn" aria-pressed={raw} onClick={onRaw}>
            {raw ? 'Show the tree' : 'Show the raw text'}
          </button>
        </div>
      )}

      {view === 'body' && asTree && <JsonTree value={value} emphasizeTop={isNdjson(answer)} />}

      {view === 'body' && !asTree && <pre class="raw mono">{answer.body}</pre>}

      {view === 'headers' && (
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

      {meta.length > 0 && (
        <dl class="resp-meta">
          {meta.map(item => (
            <div>
              <dt>{item.label}</dt>
              <dd class="mono">{item.value}</dd>
            </div>
          ))}
        </dl>
      )}

      <span class="hint">
        The log lines of the daemon have the X-Request-Id as request_id. NDJSON answers show one node per line. Open a row in <a href={browserHref}>Explorer</a> to edit it.
      </span>
    </>
  );
};

const HistoryList: FC<{ entries: Entry[]; onPick: (entry: Entry) => void }> = ({ entries, onPick }) => (
  <div class="popover" role="menu">
    {entries.length === 0 && <p class="hint">No requests yet. Each request that you send is kept in this browser.</p>}

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

const SearchIcon: FC = () => (
  <svg class="icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
    <circle cx="11" cy="11" r="6" />
    <path d="M20 20l-4-4" />
  </svg>
);

const Library: FC<{ groups: ExampleGroup[]; search: string; active?: string; onSearch: (text: string) => void; onPick: (example: Example) => void }> = ({ groups, search, active, onSearch, onPick }) => {
  const found = findExamples(groups, search);

  return (
    <nav class="dt-lib" aria-label="Examples">
      <label class="dt-search">
        <SearchIcon />

        <input
          type="search"
          value={search}
          placeholder="Find an example"
          aria-label="Find an example"
          {...noSpelling}
          onInput={(event: Event) => onSearch((event.target as HTMLInputElement).value)}
        />
      </label>

      {found.length === 0 && <p class="hint">No example has this text.</p>}

      {found.map(group => (
        <div class="dt-group">
          <span class="side-label">{group.label}</span>

          {group.items.map(example => (
            <button
              type="button"
              class={example.id === active ? 'dt-example on' : 'dt-example'}
              aria-current={example.id === active ? 'true' : undefined}
              onClick={() => onPick(example)}
            >
              <span class="grow">{example.label}</span>
              {example.fresh && <span class="dt-new">new</span>}
            </button>
          ))}
        </div>
      ))}
    </nav>
  );
};

// exampleOf finds the example that holds the request line of a block.
export function exampleOf(groups: ExampleGroup[], block?: Block): Example | undefined {
  if (!block) return undefined;

  const line = `${block.method} ${block.path}`;

  return groups.flatMap(group => group.items).find(example => example.text.split('\n').includes(line));
}

function offsetOfLine(text: string, line: number): number {
  return text.split('\n').slice(0, line).reduce((sum, row) => sum + row.length + 1, 0);
}

export const Devtools: FC<DevtoolsProps> = ({ name, collection, sendUrl, browserHref, curl }) => {
  const [text, setText] = useLocalStorage(`ndb-console:${name}:devtools:text`, defaultText(collection));
  const [history, setHistory] = useLocalStorage<Entry[]>(`ndb-console:${name}:devtools:history`, []);
  const [line, setLine] = useState(0);
  const [search, setSearch] = useState('');

  const [answer, setAnswer] = useState<Answer | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [view, setView] = useState<View>('body');
  const [raw, setRaw] = useState(false);

  const [running, setRunning] = useState(false);
  const [menu, setMenu] = useState(false);
  const [toast, setToast] = useState('');

  const area = useRef<HTMLTextAreaElement | null>(null);
  const scroller = useRef<HTMLDivElement | null>(null);
  const popover = useRef<HTMLDivElement | null>(null);

  const send = useSend(sendUrl);

  useOutsideClick(popover, menu, () => setMenu(false));

  const lines = text.split('\n');
  const blocks = parseBlocks(text);
  const block = blockAt(blocks, line);
  const runs = new Set(blocks.map(item => item.start));

  const groups = exampleGroups(collection);
  const example = exampleOf(groups, block);

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

  const apply = (element: HTMLTextAreaElement, edit: { text: string; start: number; end: number }) => {
    element.value = edit.text;
    element.setSelectionRange(edit.start, edit.end);

    setText(edit.text);
    setLine(lineAt(edit.text, edit.start));
  };

  const keys = (event: KeyboardEvent) => {
    const element = area.current;
    const command = event.metaKey || event.ctrlKey;

    if (command && event.key === 'Enter') {
      event.preventDefault();
      run();

      return;
    }

    if (command && event.key === '/' && element) {
      event.preventDefault();
      apply(element, toggleComment(element.value, element.selectionStart, element.selectionEnd));

      return;
    }

    if (!element || command || event.altKey || event.isComposing) return;

    const edit = editKey(element.value, element.selectionStart, element.selectionEnd, { key: event.key, shift: event.shiftKey });

    if (!edit) return;

    event.preventDefault();
    apply(element, edit);
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

  // place puts a request in the editor and moves the cursor and the view to
  // its first line.
  const place = (request: string) => {
    const placed = placeExample(text, request);
    const first = placed.text.split('\n').findIndex((row, index) => index >= placed.line && !isComment(row));
    const target = first === -1 ? placed.line : first;

    setText(placed.text);
    setLine(target);

    setTimeout(() => {
      const offset = offsetOfLine(placed.text, target);

      area.current?.focus({ preventScroll: true });
      area.current?.setSelectionRange(offset, offset);

      if (scroller.current) scroller.current.scrollTop = Math.max(0, (placed.line - 1) * LINE_HEIGHT);
    }, 0);
  };

  const load = (entry: Entry) => {
    place(`${entry.method} ${entry.path}${entry.body ? `\n${entry.body}` : ''}`);
    setMenu(false);
  };

  return (
    <div class="dt">
      <header class="page-head">
        <div class="page-head-text">
          <h1>Dev Tools</h1>
          <p class="lead">Send any request of the HTTP API. Start from an example, then change it.</p>
        </div>

        <div class="dt-actions">
          {toast && <span class="hint dt-toast" role="status">{toast}</span>}

          <div class="pop-host" ref={popover}>
            <button type="button" class="btn" aria-expanded={menu} onClick={() => setMenu(!menu)}>
              History
            </button>

            {menu && <HistoryList entries={history} onPick={load} />}
          </div>

          <button type="button" class="btn" onClick={copyCurl}>
            Copy as curl
          </button>

          <button
            type="button"
            class="btn primary"
            title="Ctrl or Cmd + Enter sends the request under the cursor"
            disabled={running}
            onClick={run}
          >
            {running ? 'Sending…' : 'Send · Ctrl Enter'}
          </button>
        </div>
      </header>

      <div class="dt-cols">
        <Library
          groups={groups}
          search={search}
          active={example?.id}
          onSearch={setSearch}
          onPick={picked => place(picked.text)}
        />

        <div class="dt-panes">
          <section class="ed" aria-label="Request">
            <div class="ed-head">
              <span class="ed-title">Request</span>

              <span class="grow" />

              {example && <span>{example.note}</span>}

              <button type="button" class="ed-btn" onClick={format}>
                Format
              </button>
            </div>

            <div class="ed-scroll" ref={scroller}>
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

            <div class="ed-foot">
              <span>Ctrl or Cmd + Enter sends the request under the cursor</span>
              <span>Tab indents</span>
              <span>Ctrl / comments a line</span>
            </div>
          </section>

          <section class="resp" aria-label="Response">
            <ResponsePane
              answer={answer}
              failure={failure}
              view={view}
              raw={raw}
              onView={id => setView(id as View)}
              onRaw={() => setRaw(!raw)}
              browserHref={browserHref}
            />
          </section>
        </div>
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
