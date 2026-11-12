import type { FC } from 'hono/jsx';
import { useEffect, useRef, useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { type Block, blockAt, defaultText, formatBlock, isComment, lineAt, parseBlocks } from '@/components/devtools-blocks';
import { type Example, type ExampleGroup, exampleGroups, findExamples, placeExample } from '@/components/devtools-examples';
import { buildCurl } from '@/components/curl';
import { clampSplit, type Completion, completeAt, errorMarks, type Language, type Mark, offsetOfLine, type Snap, snippetOf } from '@/components/devtools-model';
import { editKey, toggleComment } from '@/components/editor-keys';
import { FindBar, Highlight, IconButton, type KeyHint, KeysButton, LINE_HEIGHT, Matches, position, Svg } from '@/components/editor-parts';
import { containerIds, flatten } from '@/components/json-model';
import { TreeRows } from '@/components/json-tree';
import { highlightHtml } from '@/ui/json-highlight';
import { useEditHistory } from '@/hooks/useEditHistory';
import { useFind } from '@/hooks/useFind';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import { useOutsideClick } from '@/hooks/useOutsideClick';
import { useSend } from '@/hooks/useSend';
import { Segmented } from '@/ui/segmented';
import { noSpelling } from '@/ui/input';

export interface DevtoolsProps {
  name: string;
  collection: string;
  collections?: string[];
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

const LANGUAGES: { id: Language; label: string }[] = [
  { id: 'curl', label: 'curl' },
  { id: 'go', label: 'Go' },
  { id: 'ruby', label: 'Ruby' },
  { id: 'ts', label: 'TypeScript' },
];

const HISTORY_LIMIT = 50;
const RAW_LINES = 5000;

const KEYS: KeyHint[] = [
  ['Ctrl Enter', 'Send this request'],
  ['Ctrl Space', 'Show suggestions'],
  ['Ctrl /', 'Comment a line'],
  ['Ctrl G', 'Go to a line'],
  ['Ctrl F', 'Find'],
  ['Ctrl Z', 'Undo'],
  ['Ctrl Shift Z', 'Redo'],
  ['Tab', 'Indent'],
];

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

// rawText is the body for the code view: pretty JSON, and NDJSON as it is.
export function rawText(answer: Answer): string {
  if (isNdjson(answer)) return answer.body.replace(/\n+$/, '');

  const value = safeParse(answer.body, undefined);

  return value === undefined ? answer.body : JSON.stringify(value, null, 2);
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

// nextBody is the body of the next page of a read: the same body with the
// cursor of the answer in after. It is undefined when there is no cursor or
// the body is not a JSON object.
export function nextBody(block: Block, answer: { headers: Record<string, string> } | null): string | undefined {
  const cursor = answer?.headers['x-ndb-next'];

  if (!cursor) return undefined;

  try {
    const body = block.body.trim() === '' ? {} : JSON.parse(block.body);

    if (body === null || typeof body !== 'object' || Array.isArray(body)) return undefined;

    return JSON.stringify({ ...body, after: cursor });
  } catch {
    return undefined;
  }
}

// metaOf picks the headers that the response shows as chips: the request id
// first (the log lines of the daemon have it as request_id), then the
// X-Ndb-* headers of a read (plan, total, examined, next).
export function metaOf(headers: Record<string, string>): { label: string; value: string }[] {
  const name = (key: string) => key.split('-').map(part => part.charAt(0).toUpperCase() + part.slice(1)).join('-');
  const keys = Object.keys(headers).filter(key => key.startsWith('x-ndb-'));

  return ['x-request-id', ...keys].filter(key => headers[key] !== undefined).map(key => ({ label: name(key), value: headers[key] as string }));
}

interface GutterProps {
  count: number;
  block?: Block;
  runs: Block[];
  marks: Mark[];
  onRun: (block: Block) => void;
  onCurl: (block: Block) => void;
}

const Gutter: FC<GutterProps> = ({ count, block, runs, marks, onRun, onCurl }) => {
  const starts = new Map(runs.map(item => [item.start, item]));
  const errors = new Map(marks.map(mark => [mark.line, mark.message]));

  return (
    <div class="ed-gutter">
      {Array.from({ length: count }, (_, index) => {
        const inside = block !== undefined && index >= block.start && index <= block.end;
        const start = starts.get(index);
        const error = errors.get(index);

        return (
          <div class={inside ? 'ed-ln on' : 'ed-ln'}>
            {start && (
              <span class="ed-acts">
                <IconButton
                  icon="play"
                  label="Send this request"
                  tone="ed-run"
                  onClick={() => onRun(start)}
                />

                <IconButton
                  icon="copy"
                  label="Copy this request as curl"
                  tone="ed-run dim"
                  onClick={() => onCurl(start)}
                />
              </span>
            )}

            {error && (
              <span
                class="ed-err"
                role="img"
                title={error}
                aria-label={error}
              />
            )}

            <span aria-hidden="true">{index + 1}</span>
          </div>
        );
      })}
    </div>
  );
};

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

const CompleteList: FC<{ text: string; completion: Completion; active: number; onPick: (index: number) => void }> = ({ text, completion, active, onPick }) => {
  const at = position(text, completion.from);
  const style = `top:${(at.line + 1) * LINE_HEIGHT}px;left:calc(16px + ${at.column}ch)`;

  return (
    <ul
      class="ed-complete"
      role="listbox"
      aria-label="Suggestions"
      style={style}
    >
      {completion.items.map((item, index) => (
        <li
          role="option"
          aria-selected={index === active}
          class={index === active ? 'on' : ''}
          onMouseDown={(event: MouseEvent) => {
            event.preventDefault();
            onPick(index);
          }}
        >
          <span>{item.label}</span>
          {item.detail && <span class="ed-complete-what">{item.detail}</span>}
        </li>
      ))}
    </ul>
  );
};

// FACTS are the X-Ndb-* headers that read well as one short line.
const FACTS: { key: string; label: string; unit?: string }[] = [
  { key: 'x-ndb-total', label: 'total' },
  { key: 'x-ndb-plan', label: 'plan' },
  { key: 'x-ndb-examined', label: 'examined' },
  { key: 'x-ndb-took-ms', label: 'took', unit: 'ms' },
];

// Facts is the line under the status: the plan and the counts of a read,
// Previous and Next page when the daemon gave a cursor, and the request id
// (copied on a click). Every header is in the Headers view.
const Facts: FC<{ headers: Record<string, string>; onNext?: () => void; onPrev?: () => void; page: number; onCopyId: (id: string) => void }> = ({ headers, onNext, onPrev, page, onCopyId }) => {
  const facts = FACTS.filter(fact => headers[fact.key] !== undefined).map(fact => `${fact.label} ${headers[fact.key]}${fact.unit ? ` ${fact.unit}` : ''}`);
  const id = headers['x-request-id'];

  return (
    <div class="resp-facts">
      {facts.length > 0 && <span class="resp-facts-text mono">{facts.join(' · ')}</span>}

      <span class="grow" />

      {onPrev && (
        <button type="button" class="tbtn" onClick={onPrev}>
          ‹ Previous
        </button>
      )}

      {(onPrev || (onNext && headers['x-ndb-next'])) && <span class="resp-page">Page {page}</span>}

      {onNext && headers['x-ndb-next'] && (
        <button type="button" class="tbtn" title={`after: ${headers['x-ndb-next']}`} onClick={onNext}>
          Next ›
        </button>
      )}

      {id && (
        <button type="button" class="tbtn mono" title="The log lines of the daemon have this value as request_id. Click to copy." onClick={() => onCopyId(id)}>
          id {id}
        </button>
      )}

    </div>
  );
};

const CodeView: FC<{ text: string }> = ({ text }) => {
  const lines = text.split('\n');
  const shown = lines.slice(0, RAW_LINES);

  return (
    <div class="cv mono">
      {shown.map((line, index) => (
        <div class="cv-row">
          <span class="cv-ln" aria-hidden="true">{index + 1}</span>
          <span class="cv-code" dangerouslySetInnerHTML={{ __html: highlightHtml(line) || '​' }} />
        </div>
      ))}

      {lines.length > RAW_LINES && <p class="hint cv-more">The view shows the first {RAW_LINES} lines. Copy gets the whole body.</p>}
    </div>
  );
};

interface ResponseProps {
  answer: Answer | null;
  failure: Failure | null;
  view: View;
  closed: Set<string>;
  onView: (id: string) => void;
  onClosed: (next: Set<string>) => void;
  onCopy: () => void;
  onNext?: () => void;
  onPrev?: () => void;
  page: number;
  onCopyId: (id: string) => void;
}

const ResponsePane: FC<ResponseProps> = ({ answer, failure, view, closed, onView, onClosed, onCopy, onNext, onPrev, page, onCopyId }) => {
  if (failure) return <p class="note bad">{failure.message}</p>;

  if (!answer) return <p class="hint">Send a request to see the answer here.</p>;

  const value = treeValue(answer);
  const treeable = value !== undefined && typeof value === 'object' && value !== null;
  const shown: View = view === 'tree' && !treeable ? 'raw' : view;
  const label = `${answer.status} ${STATUS_TEXT[answer.status] ?? ''}`.trim();

  const views = [...(treeable ? [{ id: 'tree', label: 'Tree' }] : []), { id: 'raw', label: 'Raw' }, { id: 'headers', label: 'Headers' }];

  return (
    <>
      <div class="resp-head">
        <span class={`status ${statusTone(answer.status)}`}>{label}</span>
        <span class="hint resp-size">{describe(answer)}</span>

        <span class="grow" />

        <Segmented
          items={views}
          value={shown}
          label="View"
          onSelect={onView}
        />

        {shown === 'tree' && (
          <>
            <IconButton icon="expand" label="Expand all" tone="tbtn icon" onClick={() => onClosed(new Set())} />
            <IconButton icon="collapse" label="Collapse" tone="tbtn icon" onClick={() => onClosed(new Set(containerIds(value)))} />
          </>
        )}

        <IconButton icon="copy" label="Copy the body" tone="tbtn icon" onClick={onCopy} />
      </div>

      <Facts headers={answer.headers} onNext={onNext} onPrev={onPrev} page={page} onCopyId={onCopyId} />

      {answer.truncated && <p class="note bad">The answer is cut at 4 MiB. Add a limit to the request.</p>}

      <div class="resp-body">
        {shown === 'tree' && (
          <div class="jt">
            <TreeRows
              rows={flatten(value, closed)}
              emphasizeTop={isNdjson(answer)}
              onToggle={row => {
                const next = new Set(closed);

                if (next.has(row.id)) next.delete(row.id);
                else next.add(row.id);

                onClosed(next);
              }}
            />
          </div>
        )}

        {shown === 'raw' && <CodeView text={rawText(answer)} />}

        {shown === 'headers' && (
          <dl class="resp-headers" aria-label="Response headers">
            {Object.entries(answer.headers).map(([key, item]) => (
              <div class="resp-header">
                <dt class="mono">{key}</dt>

                <dd class="mono" title={item}>
                  {item}
                </dd>

                <IconButton icon="copy" label={`Copy ${key}`} tone="tbtn icon" onClick={() => onCopyId(item)} />
              </div>
            ))}
          </dl>
        )}
      </div>
    </>
  );
};

// whenOf is the short time of a history entry: the clock time today, else
// the day and the time.
export function whenOf(at: number, now = Date.now()): string {
  const date = new Date(at);
  const time = date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
  const today = new Date(now).toDateString() === date.toDateString();

  return today ? time : `${date.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' })} ${time}`;
}

const HistoryList: FC<{ entries: Entry[]; onPick: (entry: Entry) => void; onClear: () => void }> = ({ entries, onPick, onClear }) => (
  <div class="popover history" role="menu">
    <div class="history-head">
      <span class="side-label">Recent requests</span>

      {entries.length > 0 && (
        <button type="button" class="tbtn" onClick={onClear}>
          Clear
        </button>
      )}
    </div>

    {entries.length === 0 && <p class="hint">No requests yet. Each request that you send is kept in this browser.</p>}

    {entries.map(entry => (
      <button
        type="button"
        class="history-item"
        role="menuitem"
        title={`${entry.method} ${entry.path}`}
        onClick={() => onPick(entry)}
      >
        <span class={`status small ${statusTone(entry.status)}`}>{entry.status}</span>
        <span class="history-method mono">{entry.method}</span>
        <span class="history-path mono">{entry.path}</span>
        <span class="history-when">{whenOf(entry.at)}</span>
      </button>
    ))}
  </div>
);

const CopyMenu: FC<{ block?: Block; target: { socket?: string; base?: string }; onPick: (language: Language) => void }> = ({ block, target, onPick }) => (
  <div class="popover dt-copy" role="menu">
    {LANGUAGES.map(language => {
      const ready = block !== undefined && (language.id === 'curl' || snippetOf(language.id, { ...target, method: block.method, path: block.path, body: block.body }) !== undefined);

      return (
        <button
          type="button"
          class="history-item"
          role="menuitem"
          disabled={!ready}
          title={ready ? undefined : 'This SDK has no call for this route.'}
          onClick={() => onPick(language.id)}
        >
          {language.label}
        </button>
      );
    })}
  </div>
);

const Library: FC<{ groups: ExampleGroup[]; search: string; active?: string; onSearch: (text: string) => void; onPick: (example: Example) => void }> = ({ groups, search, active, onSearch, onPick }) => {
  const found = findExamples(groups, search);

  return (
    <nav class="popover dt-lib" aria-label="Examples">
      <label class="dt-search">
        <Svg name="search" size={16} />

        <input
          type="search"
          value={search}
          placeholder="Find an example"
          aria-label="Find an example"
          {...noSpelling}
          autofocus
          onInput={(event: Event) => onSearch((event.target as HTMLInputElement).value)}
        />
      </label>

      {found.length === 0 && <p class="hint">No example has this text.</p>}

      <div class="dt-lib-groups">
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
      </div>
    </nav>
  );
};

// exampleOf finds the example that holds the request line of a block.
export function exampleOf(groups: ExampleGroup[], block?: Block): Example | undefined {
  if (!block) return undefined;

  const line = `${block.method} ${block.path}`;

  return groups.flatMap(group => group.items).find(example => example.text.split('\n').includes(line));
}

export const Devtools: FC<DevtoolsProps> = ({ name, collection, collections = [], sendUrl, curl }) => {
  const [text, setText] = useLocalStorage(`ndb-console:${name}:devtools:text`, defaultText(collection));
  const [history, setHistory] = useLocalStorage<Entry[]>(`ndb-console:${name}:devtools:history`, []);
  const [split, setSplit] = useLocalStorage(`ndb-console:devtools:split`, 50);
  const [line, setLine] = useState(0);
  const [search, setSearch] = useState('');

  const [answer, setAnswer] = useState<Answer | null>(null);
  const [sent, setSent] = useState<Block | null>(null);
  const [pages, setPages] = useState<Block[]>([]);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [view, setView] = useState<View>('tree');
  const [closed, setClosed] = useState<Set<string>>(new Set());

  const [running, setRunning] = useState(false);
  const [menu, setMenu] = useState(false);
  const [library, setLibrary] = useState(false);
  const [copyMenu, setCopyMenu] = useState(false);
  const [toast, setToast] = useState('');

  const [completion, setCompletion] = useState<Completion | null>(null);
  const [active, setActive] = useState(0);

  const root = useRef<HTMLDivElement | null>(null);
  const panes = useRef<HTMLDivElement | null>(null);
  const area = useRef<HTMLTextAreaElement | null>(null);
  const scroller = useRef<HTMLDivElement | null>(null);
  const examples = useRef<HTMLDivElement>(null);
  const popover = useRef<HTMLDivElement | null>(null);
  const copyHost = useRef<HTMLDivElement | null>(null);

  const send = useSend(sendUrl);

  useOutsideClick(popover, menu, () => setMenu(false));
  useOutsideClick(examples, library, () => setLibrary(false));
  useOutsideClick(copyHost, copyMenu, () => setCopyMenu(false));

  useEffect(() => {
    const fit = () => {
      const element = root.current;

      if (!element) return;

      const top = element.getBoundingClientRect().top + window.scrollY;
      const main = element.closest('main');
      const below = main ? parseFloat(getComputedStyle(main).paddingBottom) || 0 : 24;

      element.style.setProperty('--dt-h', `${Math.max(420, window.innerHeight - top - below)}px`);
    };

    fit();
    window.addEventListener('resize', fit);

    return () => window.removeEventListener('resize', fit);
  }, []);

  const lines = text.split('\n');
  const blocks = parseBlocks(text);
  const block = blockAt(blocks, line);
  const marks = errorMarks(text, blocks);

  const groups = exampleGroups(collection);
  const example = exampleOf(groups, block);

  const show = (snap: Snap) => {
    setText(snap.text);
    setLine(lineAt(snap.text, snap.start));
  };

  const edits = useEditHistory(area, text, show);
  const find = useFind(area, scroller, text, LINE_HEIGHT);

  const editorHeight = `${lines.length * LINE_HEIGHT + 28}px`;
  const panesStyle = `--dt-split:${clampSplit(split)}%`;

  const say = (message: string) => {
    setToast(message);
    setTimeout(() => setToast(''), 2500);
  };

  const track = () => {
    const element = area.current;

    if (!element) return;

    edits.track();
    setLine(lineAt(element.value, element.selectionStart));
  };

  const copyId = async (id: string) => {
    say((await copyText(id)) ? 'Copied.' : 'The browser blocked the clipboard.');
  };

  // run sends a block. trail holds the requests of the pages before this
  // one when Next page sent it, so Previous page can go back; a run from
  // the editor starts a new trail.
  const run = async (target: Block | undefined = block, trail: Block[] = []) => {
    if (!target || running) {
      if (!target) say('Write a line like GET /_stats first.');

      return;
    }

    setRunning(true);
    setFailure(null);

    const result = await send<Answer & { message?: string }>({ method: target.method, path: target.path, body: target.body });

    setRunning(false);

    if (!result.ok && result.body.status === undefined) {
      setAnswer(null);
      setFailure({ message: result.body.message ?? 'The request failed.' });

      return;
    }

    setAnswer(result.body);
    setSent(target);
    setPages(trail);
    setClosed(new Set());

    const entry: Entry = { method: target.method, path: target.path, body: target.body, status: result.body.status, at: Date.now() };

    setHistory([entry, ...history.filter(item => !(item.method === entry.method && item.path === entry.path && item.body === entry.body))].slice(0, HISTORY_LIMIT));
  };

  const change = edits.change;

  const step = (back: boolean) => {
    if (edits.step(back)) setCompletion(null);
  };

  const pick = (index: number) => {
    const element = area.current;
    const item = completion?.items[index];

    if (!element || !completion || !item) return;

    const value = element.value;
    const caret = completion.from + item.label.length;

    setCompletion(null);
    change({ text: value.slice(0, completion.from) + item.label + value.slice(completion.to), start: caret, end: caret });
  };

  const suggest = (element: HTMLTextAreaElement) => {
    const next = element.selectionStart === element.selectionEnd ? completeAt(element.value, element.selectionStart, collections) : undefined;

    setCompletion(next ?? null);
    setActive(0);
  };

  const goToLine = () => {
    const answer = window.prompt('Go to line', String(line + 1));
    const target = Number(answer);

    if (!answer || !Number.isInteger(target) || target < 1) return;

    const row = Math.min(target, lines.length) - 1;
    const offset = offsetOfLine(text, row);

    area.current?.focus();
    area.current?.setSelectionRange(offset, offset);
    setLine(row);

    if (scroller.current) scroller.current.scrollTop = Math.max(0, (row - 3) * LINE_HEIGHT);
  };

  const completeKeys = (event: KeyboardEvent): boolean => {
    if (!completion) return false;

    const size = completion.items.length;

    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      setActive((active + (event.key === 'ArrowDown' ? 1 : -1) + size) % size);

      return true;
    }

    if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault();
      pick(active);

      return true;
    }

    if (event.key === 'Escape') {
      event.preventDefault();
      setCompletion(null);

      return true;
    }

    return false;
  };

  const keys = (event: KeyboardEvent) => {
    const element = area.current;
    const command = event.metaKey || event.ctrlKey;
    const key = event.key.toLowerCase();

    if (completeKeys(event)) return;

    if (command && (key === 'z' || (key === 'y' && event.ctrlKey))) {
      event.preventDefault();
      step(key === 'z' && !event.shiftKey);

      return;
    }

    if (command && key === 'f') {
      event.preventDefault();
      find.show();

      return;
    }

    if (event.ctrlKey && key === 'g') {
      event.preventDefault();
      goToLine();

      return;
    }

    if (event.ctrlKey && event.key === ' ' && element) {
      event.preventDefault();
      suggest(element);

      return;
    }

    if (command && event.key === 'Enter') {
      event.preventDefault();
      run();

      return;
    }

    if (command && event.key === '/' && element) {
      event.preventDefault();
      change(toggleComment(element.value, element.selectionStart, element.selectionEnd));

      return;
    }

    if (!element || command || event.altKey || event.isComposing) return;

    const edit = editKey(element.value, element.selectionStart, element.selectionEnd, { key: event.key, shift: event.shiftKey });

    if (!edit) return;

    event.preventDefault();
    change(edit);

    if (event.key !== 'Enter' && event.key !== 'Tab') setTimeout(() => area.current && suggest(area.current), 0);
  };

  const typed = (event: Event) => {
    const element = event.target as HTMLTextAreaElement;

    edits.typed(event);

    setText(element.value);
    setLine(lineAt(element.value, element.selectionStart));
    suggest(element);
  };

  const format = () => {
    const formatted = block ? formatBlock(text, block) : undefined;

    if (formatted === undefined) {
      say('This block has no JSON body to format.');

      return;
    }

    const caret = area.current?.selectionStart ?? 0;

    change({ text: formatted, start: caret, end: caret });
  };

  const copyAs = async (language: Language, target: Block | undefined = block) => {
    setCopyMenu(false);

    if (!target) {
      say('Put the cursor in a request first.');

      return;
    }

    const input = { method: target.method, path: target.path, body: target.body, socket: curl.socket, base: curl.base };
    const code = language === 'curl' ? buildCurl(input) : snippetOf(language, input);

    if (code === undefined) {
      say('This SDK has no call for this route.');

      return;
    }

    const copied = await copyText(code);
    const label = LANGUAGES.find(item => item.id === language)?.label ?? language;

    say(copied ? `Copied as ${label}.` : 'The browser blocked the clipboard.');
  };

  const copyBody = async () => {
    if (!answer) return;

    say((await copyText(answer.body)) ? 'Copied the body.' : 'The browser blocked the clipboard.');
  };

  // place puts a request in the editor and moves the cursor and the view to
  // its first line.
  const place = (request: string) => {
    const placed = placeExample(text, request);
    const first = placed.text.split('\n').findIndex((row, index) => index >= placed.line && !isComment(row));
    const target = first === -1 ? placed.line : first;
    const offset = offsetOfLine(placed.text, target);

    change({ text: placed.text, start: offset, end: offset });

    setTimeout(() => {
      area.current?.focus({ preventScroll: true });
      area.current?.setSelectionRange(offset, offset);

      if (scroller.current) scroller.current.scrollTop = Math.max(0, (placed.line - 1) * LINE_HEIGHT);
    }, 0);
  };

  const load = (entry: Entry) => {
    place(`${entry.method} ${entry.path}${entry.body ? `\n${entry.body}` : ''}`);
    setMenu(false);
  };

  const drag = (event: PointerEvent) => {
    const handle = event.currentTarget as HTMLElement;
    const box = panes.current?.getBoundingClientRect();

    if (!box) return;

    handle.setPointerCapture(event.pointerId);

    const move = (next: PointerEvent) => setSplit(clampSplit(((next.clientX - box.left) / box.width) * 100));

    const stop = () => {
      handle.removeEventListener('pointermove', move);
      handle.removeEventListener('pointerup', stop);
      handle.removeEventListener('pointercancel', stop);
    };

    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', stop);
    handle.addEventListener('pointercancel', stop);
  };

  const dragKeys = (event: KeyboardEvent) => {
    const steps: Record<string, number> = { ArrowLeft: -5, ArrowRight: 5, Home: -100, End: 100 };
    const delta = steps[event.key];

    if (delta === undefined) return;

    event.preventDefault();
    setSplit(clampSplit(clampSplit(split) + delta));
  };

  return (
    <div class="dt" ref={root}>
      <header class="dt-bar">
        <h1 class="dt-title">Dev Tools</h1>

        {toast && <span class="hint dt-toast" role="status">{toast}</span>}

        <span class="grow" />

        <div class="pop-host" ref={examples}>
          <button type="button" class="btn small" aria-expanded={library} onClick={() => setLibrary(!library)}>
            Examples
            {example && <span class="dt-current">{example.label}</span>}
          </button>

          {library && (
            <Library
              groups={groups}
              search={search}
              active={example?.id}
              onSearch={setSearch}
              onPick={picked => {
                place(picked.text);
                setLibrary(false);
              }}
            />
          )}
        </div>

        <div class="pop-host" ref={popover}>
          <button type="button" class="btn small" aria-expanded={menu} onClick={() => setMenu(!menu)}>
            History
          </button>

          {menu && <HistoryList entries={history} onPick={load} onClear={() => setHistory([])} />}
        </div>

        <div class="pop-host" ref={copyHost}>
          <button type="button" class="btn small" aria-expanded={copyMenu} onClick={() => setCopyMenu(!copyMenu)}>
            Copy as
          </button>

          {copyMenu && (
            <CopyMenu
              block={block}
              target={curl}
              onPick={copyAs}
            />
          )}
        </div>

        <button
          type="button"
          class="btn small primary"
          title="Ctrl or Cmd + Enter sends the request under the cursor"
          disabled={running}
          onClick={() => run()}
        >
          {running ? 'Sending…' : 'Send · Ctrl Enter'}
        </button>
      </header>

      <div class="dt-panes" ref={panes} style={panesStyle}>
        <section class="ed" aria-label="Request">
          <div class="ed-head">
            <span class="ed-title">Request</span>

            <IconButton icon="undo" label="Undo (Ctrl or Cmd + Z)" disabled={!edits.canUndo} onClick={() => step(true)} />
            <IconButton icon="redo" label="Redo (Ctrl or Cmd + Shift + Z)" disabled={!edits.canRedo} onClick={() => step(false)} />
            <IconButton icon="search" label="Find (Ctrl or Cmd + F)" onClick={find.show} />
            <KeysButton items={KEYS} />

            <span class="grow" />

            {example && <span class="ed-note">{example.note}</span>}

            <button type="button" class="ed-btn" onClick={format}>
              Format
            </button>
          </div>

          {find.open && (
            <FindBar
              query={find.query}
              count={find.found.length}
              current={find.current}
              onQuery={find.search}
              onMove={find.move}
              onClose={find.close}
            />
          )}

          <div class="ed-scroll" ref={scroller}>
            <Gutter
              count={lines.length}
              block={block}
              runs={blocks}
              marks={marks}
              onRun={target => run(target)}
              onCurl={target => copyAs('curl', target)}
            />

            <div class="ed-main">
              <Highlight lines={lines} on={block} marks={marks} />

              {find.found.length > 0 && <Matches text={text} found={find.found} size={find.query.length} current={find.current} />}

              <textarea
                ref={area}
                class="ed-ta"
                style={`height:${editorHeight}`}
                value={text}
                {...noSpelling}
                wrap="off"
                aria-label="Requests"
                aria-autocomplete="list"
                onInput={typed}
                onKeyDown={keys}
                onKeyUp={track}
                onClick={() => {
                  track();
                  setCompletion(null);
                }}
                onFocus={track}
                onBlur={() => setCompletion(null)}
              />

              {block?.body ? <FormatButton line={block.start} onClick={format} /> : null}

              {completion && (
                <CompleteList
                  text={text}
                  completion={completion}
                  active={active}
                  onPick={pick}
                />
              )}
            </div>
          </div>

        </section>

        <div
          class="dt-handle"
          role="separator"
          tabindex={0}
          aria-orientation="vertical"
          aria-label="Resize the request and the response"
          aria-valuemin={25}
          aria-valuemax={75}
          aria-valuenow={clampSplit(split)}
          onPointerDown={drag}
          onKeyDown={dragKeys}
        />

        <section class="resp" aria-label="Response">
          <ResponsePane
            answer={answer}
            failure={failure}
            view={view}
            closed={closed}
            onView={id => setView(id as View)}
            onClosed={setClosed}
            onCopy={copyBody}
            onNext={sent && nextBody(sent, answer) ? () => run({ ...sent, body: nextBody(sent, answer) as string }, [...pages, sent]) : undefined}
            onPrev={pages.length > 0 ? () => run(pages[pages.length - 1], pages.slice(0, -1)) : undefined}
            page={pages.length + 1}
            onCopyId={copyId}
          />
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
