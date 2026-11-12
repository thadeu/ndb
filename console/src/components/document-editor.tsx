import type { FC } from 'hono/jsx';
import { useEffect, useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { DocFacts } from '@/components/document-facts';
import { DOC_READY, DOC_SAVED } from '@/components/document-drawer';
import { byteSize, describeDiff, diffDocs, isEmptyDiff } from '@/components/json-model';
import { JsonEditor } from '@/components/json-editor';
import { jsonMark } from '@/components/devtools-model';
import { JsonTree } from '@/components/json-tree';
import { useSend } from '@/hooks/useSend';
import { confirm } from '@/ui/alert-dialog';
import { Button } from '@/ui/button';
import { Segmented } from '@/ui/segmented';

export interface EditorProps {
  coll: string;
  id: string;
  doc: Record<string, unknown>;
  saveUrl: string;
  lockUrl: string;
  unlockUrl: string;
  deleteUrl: string;
  find: string;
  lockedUntil?: string;
  writable: boolean;
  reason?: string;
  notice?: string;
  problem?: string;
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

interface LockRowProps {
  until: string | null;
  busy: boolean;
  onLock: () => void;
  onUnlock: () => void;
}

// LockRow shows the pessimistic lock of the document. The lock token is in
// the console server; this row knows only the time of the end.
const LockRow: FC<LockRowProps> = ({ until, busy, onLock, onUnlock }) => (
  <div class="doc-lock">
    {until ? (
      <span class="doc-lock-text">
        <strong>Locked by this console</strong> until {new Date(until).toLocaleTimeString()}.
      </span>
    ) : (
      <span class="doc-lock-text">
        <strong>Not locked.</strong> A save checks <code class="mono">_v</code> (optimistic).
      </span>
    )}

    <Button size="sm" type="button" disabled={busy} onClick={until ? onUnlock : onLock}>
      {until ? 'Unlock' : 'Lock for 30 s'}
    </Button>
  </div>
);

// startNotice is the message that the page was loaded with, such as "Saved"
// after a save, so it shows in the drawer and not only behind it.
function startNotice(notice?: string, problem?: string): Notice | null {
  if (problem) return { tone: 'bad', text: problem };

  return notice ? { tone: 'ok', text: notice } : null;
}

// DocumentEditor is the content of the document drawer, in two columns. The
// first has the fields: the selected document as a tree with a filter box,
// or as JSON. The second has the facts of the document, the lock row, the
// messages, and the actions. Save sends the difference as $set and $unset,
// guarded by the _v that the page was loaded with.
export const DocumentEditor: FC<EditorProps> = props => {
  const { coll, id, doc: loaded, saveUrl, lockUrl, unlockUrl, deleteUrl, find, lockedUntil, writable, reason } = props;

  const [mode, setMode] = useState<Mode>('tree');
  const [doc, setDoc] = useState<Record<string, unknown>>(loaded);
  const [draft, setDraft] = useState<Record<string, unknown>>(doc);
  const [text, setText] = useState(pretty(doc));
  const [notice, setNotice] = useState<Notice | null>(startNotice(props.notice, props.problem));
  const [saving, setSaving] = useState(false);
  const [locked, setLocked] = useState<string | null>(lockedUntil ?? null);
  const [locking, setLocking] = useState(false);

  const send = useSend(saveUrl);
  const sendLock = useSend(lockUrl);
  const sendUnlock = useSend(unlockUrl);

  useEffect(() => {
    if (!locked) return;

    const left = new Date(locked).getTime() - Date.now();
    const timer = setTimeout(() => setLocked(null), Math.max(0, left));

    return () => clearTimeout(timer);
  }, [locked]);

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

  const lock = async () => {
    setLocking(true);

    const answer = await sendLock<{ message?: string; until?: string; _v?: number }>({ coll, id });

    setLocking(false);

    if (!answer.ok || !answer.body.until) {
      setNotice({ tone: 'bad', text: answer.body.message ?? 'The lock failed.' });

      return;
    }

    setLocked(answer.body.until);

    const stale = answer.body._v !== undefined && answer.body._v !== version;

    setNotice(stale ? { tone: 'bad', text: 'Locked, but somebody else changed this document before the lock; reload.', reload: true } : null);
  };

  const unlock = async () => {
    setLocking(true);

    await sendUnlock({ coll, id });

    setLocking(false);
    setLocked(null);
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

    const answer = await send<{ message?: string; conflict?: boolean; lockEnded?: boolean; doc?: Record<string, unknown> }>({ coll, id, _v: version, set: diff.set, unset: diff.unset });

    setSaving(false);

    if (answer.ok && answer.body.doc) {
      const saved = { _id: answer.body.doc._id, _v: answer.body.doc._v, ...answer.body.doc };

      setDoc(saved);
      setDraft(saved);
      setText(pretty(saved));
      setNotice({ tone: 'ok', text: `Saved. Now _v ${String(saved._v)}.` });
      document.dispatchEvent(new CustomEvent(DOC_SAVED, { detail: { id } }));

      return;
    }

    if (answer.body.conflict) {
      setNotice({ tone: 'bad', text: 'Somebody else changed this document; reload.', reload: true });

      return;
    }

    if (answer.body.lockEnded) setLocked(null);

    setNotice({ tone: 'bad', text: answer.body.message ?? 'The save failed.' });
  };

  const label = dirty && !isEmptyDiff(diff) ? `Save · ${describeDiff(diff)}` : 'Save';
  const canSave = writable && dirty && !saving && typeof parsed !== 'string';
  const hint = hintOf(writable, reason, dirty, diff.set, diff.unset);
  const notObject = mode === 'json' && typeof parsed === 'string' && jsonMark(text) === undefined;
  const ask = confirm({ title: 'Delete this document?', body: `${id} is removed from ${coll}. A watch consumer sees a del event.`, action: 'Delete' });

  return (
    <>
      <section class="doc-fields" aria-label="Fields">
        <div class="doc-fields-head">
          <Segmented items={MODES} value={mode} label="View" onSelect={changeMode} />
        </div>

        {mode === 'tree' ? (
          <JsonTree
            value={draft}
            editable={writable}
            filterable
            baseline={doc}
            onChange={next => setDraft(next as Record<string, unknown>)}
          />
        ) : (
          <JsonEditor
            value={text}
            label="Document JSON"
            readOnly={!writable}
            onChange={setText}
            onSubmit={() => canSave && save()}
          />
        )}

        {notObject && <p class="note bad">{parsed}</p>}
      </section>

      <div class="doc-side">
        <DocFacts id={id} version={String(version)} bytes={byteSize(doc)} />

        {writable && <LockRow until={locked} busy={locking} onLock={lock} onUnlock={unlock} />}

        {notice && <NoticeLine notice={notice} />}

        <p class="hint">{hint}</p>

        <div class="doc-actions">
          <div class="doc-actions-row">
            <Button type="button" size="sm" variant="ghost" onClick={copy}>
              Copy JSON
            </Button>

            {writable && (
              <form class="doc-delete" method="post" action={deleteUrl} {...ask}>
                <input type="hidden" name="coll" value={coll} />
                <input type="hidden" name="id" value={id} />
                <input type="hidden" name="find" value={find} />

                <Button variant="ghost" size="sm" class="doc-delete-btn" type="submit">
                  Delete
                </Button>
              </form>
            )}
          </div>

          {writable && (
            <div class="doc-actions-main">
              <Button type="button" disabled={!dirty} onClick={discard}>
                Discard
              </Button>

              <Button variant="dark" type="button" class="doc-save" disabled={!canSave} onClick={save}>
                {saving ? 'Saving…' : 'Save'}
              </Button>
            </div>
          )}

          {writable && dirty && <p class="doc-change">{label.replace('Save · ', 'Changes: ')}</p>}
        </div>
      </div>
    </>
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

// The script of the rows (document-drawer.ts) puts a new document in the
// drawer without a page load, and then sends this event.
if (typeof document !== 'undefined') {
  mount();

  document.addEventListener(DOC_READY, mount);
}
