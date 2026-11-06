import type { FC } from 'hono/jsx';
import { useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { byteSize, deepEqual, describeDiff, diffDocs, isEmptyDiff } from '@/components/json-model';
import { JsonTree } from '@/components/json-tree';
import { useSend } from '@/hooks/useSend';
import { Segmented } from '@/ui/segmented';
import { noSpelling } from '@/ui/input';

export interface EditorProps {
  coll: string;
  id: string;
  doc: Record<string, unknown>;
  saveUrl: string;
  writable: boolean;
  reason?: string;
}

type Mode = 'tree' | 'json';

interface Notice {
  tone: 'ok' | 'bad';
  text: string;
  reload?: boolean;
}

const MODES = [
  { id: 'tree', label: 'Tree' },
  { id: 'json', label: 'JSON' },
];

const pretty = (value: unknown) => JSON.stringify(value, null, 2);

function parseObject(text: string): Record<string, unknown> | string {
  try {
    const value = JSON.parse(text);

    if (value === null || typeof value !== 'object' || Array.isArray(value)) return 'The document must be a JSON object.';

    return value as Record<string, unknown>;
  } catch (error) {
    return error instanceof Error ? error.message : 'The JSON is not valid.';
  }
}

const NoticeLine: FC<{ notice: Notice }> = ({ notice }) => (
  <p class={notice.tone === 'ok' ? 'note ok' : 'note bad'} role="status">
    {notice.text}

    {notice.reload && (
      <>
        {' '}
        <a href={window.location.href}>Reload</a>
      </>
    )}
  </p>
);

// DocumentEditor is the right panel of the Browser: the selected document as
// a tree or as JSON. Save sends the difference as $set and $unset, guarded by
// the _v that the page was loaded with.
export const DocumentEditor: FC<EditorProps> = ({ coll, id, doc, saveUrl, writable, reason }) => {
  const [mode, setMode] = useState<Mode>('tree');
  const [draft, setDraft] = useState<Record<string, unknown>>(doc);
  const [text, setText] = useState(pretty(doc));
  const [notice, setNotice] = useState<Notice | null>(null);
  const [saving, setSaving] = useState(false);

  const send = useSend(saveUrl);

  const version = Number(doc._v ?? 0);
  const parsed = mode === 'json' ? parseObject(text) : draft;
  const current = typeof parsed === 'string' ? draft : parsed;
  const diff = diffDocs(doc, current);
  const dirty = !isEmptyDiff(diff) || diff.blocked.length > 0;

  const changeMode = (next: string) => {
    if (next === mode) return;

    if (next === 'json') {
      setText(pretty(draft));
      setMode('json');

      return;
    }

    if (typeof parsed === 'string') {
      setNotice({ tone: 'bad', text: parsed });

      return;
    }

    setDraft(parsed);
    setNotice(null);
    setMode('tree');
  };

  const discard = () => {
    setDraft(doc);
    setText(pretty(doc));
    setNotice(null);
  };

  const copy = async () => {
    const copied = await copyText(pretty(current));

    setNotice({ tone: copied ? 'ok' : 'bad', text: copied ? 'Copied the JSON.' : 'The browser blocked the clipboard.' });
  };

  const save = async () => {
    if (typeof parsed === 'string') {
      setNotice({ tone: 'bad', text: parsed });

      return;
    }

    if (diff.blocked.length > 0) {
      setNotice({ tone: 'bad', text: `Cannot save a key with a dot or a leading $: ${diff.blocked.join(', ')}.` });

      return;
    }

    setSaving(true);

    const answer = await send<{ message?: string; conflict?: boolean }>({ coll, id, _v: version, set: diff.set, unset: diff.unset });

    setSaving(false);

    if (answer.ok) {
      const url = new URL(window.location.href);

      url.searchParams.set('done', 'saved');
      url.searchParams.delete('insert');
      window.location.assign(url);

      return;
    }

    if (answer.body.conflict) {
      setNotice({ tone: 'bad', text: 'Somebody else changed this document; reload.', reload: true });

      return;
    }

    setNotice({ tone: 'bad', text: answer.body.message ?? 'The save failed.' });
  };

  const label = dirty && !isEmptyDiff(diff) ? `Save · ${describeDiff(diff)}` : 'Save';
  const canSave = writable && dirty && !saving && typeof parsed !== 'string';
  const hint = hintOf(writable, reason, dirty, diff.set, diff.unset);

  return (
    <div class="doc-editor">
      <div class="doc-head">
        <span class="doc-id mono">{id}</span>
        <span class="doc-meta">
          _v {version} · {byteSize(doc)} B
        </span>

        <span class="grow" />

        <Segmented items={MODES} value={mode} label="View" onSelect={changeMode} />
      </div>

      {mode === 'tree' ? (
        <JsonTree value={draft} editable={writable} baseline={doc} onChange={next => setDraft(next as Record<string, unknown>)} />
      ) : (
        <textarea class="textarea mono doc-json" {...noSpelling} aria-label="Document JSON" readOnly={!writable} value={text} onInput={(event: Event) => setText((event.target as HTMLTextAreaElement).value)} />
      )}

      {typeof parsed === 'string' && mode === 'json' && <p class="note bad">{parsed}</p>}

      {notice && <NoticeLine notice={notice} />}

      <div class="doc-actions">
        {writable && (
          <button type="button" class="btn primary small" disabled={!canSave} onClick={save}>
            {label}
          </button>
        )}

        {writable && (
          <button type="button" class="btn small" disabled={deepEqual(current, doc) && !dirty} onClick={discard}>
            Discard
          </button>
        )}

        <button type="button" class="btn small" onClick={copy}>
          Copy JSON
        </button>
      </div>

      <span class="hint">{hint}</span>
    </div>
  );
};

function hintOf(writable: boolean, reason: string | undefined, dirty: boolean, set: Record<string, unknown>, unset: string[]): string {
  if (!writable) return reason ?? 'This collection is read-only.';

  if (!dirty) return 'Click a value to edit it. Save sends only the changed fields, with _v as a guard, so a concurrent write is not lost.';

  return `${Object.keys(set).length} set · ${unset.length} unset. Save sends POST …/update with _v as a guard.`;
}

// mount replaces the server tree with the editor. It reads the document from
// the page, so the first paint needs no request.
export function mount(): void {
  const host = document.getElementById('doc-editor');

  if (!host || host.dataset.mounted) return;

  host.dataset.mounted = 'true';

  const props = JSON.parse(host.dataset.props ?? '{}') as EditorProps;

  host.replaceChildren();

  render(<DocumentEditor {...props} />, host);
}

if (typeof document !== 'undefined') mount();
