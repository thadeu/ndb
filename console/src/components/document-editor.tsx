import type { FC } from 'hono/jsx';
import { useEffect, useState } from 'hono/jsx';
import { render } from 'hono/jsx/dom';

import { copyText } from '@/components/copy';
import { DocFacts } from '@/components/document-facts';
import { DOC_READY, DOC_SAVED } from '@/components/document-drawer';
import { byteSize, describeDiff, diffDocs, isEmptyDiff, parseScalar, type Row } from '@/components/json-model';
import { JsonEditor } from '@/components/json-editor';
import { jsonMark } from '@/components/devtools-model';
import { JsonTree } from '@/components/json-tree';
import { deniedText, keepSealed, maskDoc, SEALED_MASK, SHOW_SECONDS } from '@/components/sealed-model';
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
  sealed?: string[];
  showUrl?: string;
}

interface Shown {
  field: string;
  text: string;
}

type Mode = 'tree' | 'json';

interface Notice {
  tone: 'ok' | 'bad';
  text: string;
  reload?: boolean;
}

const JSON_NOTE = `Encrypted values show as "${SEALED_MASK}". Keep that text to keep the stored value, or type a new value to replace it.`;

const MODES = [
  { id: 'tree', label: 'Tree' },
  { id: 'json', label: 'JSON' },
];

const pretty = (value: unknown) => JSON.stringify(value, null, 2);

const plainText = (value: unknown) => (typeof value === 'string' ? value : JSON.stringify(value));

interface SealActionsProps {
  row: Row;
  shown: Shown | null;
  writable: boolean;
  busy: boolean;
  onShow: (field: string) => void;
  onHide: () => void;
  onCopy: (text: string) => void;
  onReplace: (field: string) => void;
}

// SealActions are the buttons of an encrypted field in the tree: Show reads
// the plain value for a short time, Replace value writes a new one.
const SealActions: FC<SealActionsProps> = ({ row, shown, writable, busy, onShow, onHide, onCopy, onReplace }) => {
  const field = row.key;

  if (shown?.field === field) {
    return (
      <span class="seal-actions">
        <code class="seal-shown mono">{shown.text}</code>

        <button type="button" class="tbtn" onClick={() => onCopy(shown.text)}>
          Copy
        </button>

        <button type="button" class="tbtn" onClick={onHide}>
          Hide
        </button>
      </span>
    );
  }

  return (
    <span class="seal-actions">
      <button
        type="button"
        class="tbtn"
        disabled={busy}
        aria-label={`Show the value of ${field}`}
        onClick={() => onShow(field)}
      >
        Show
      </button>

      {writable && (
        <button
          type="button"
          class="tbtn"
          aria-label={`Replace the value of ${field}`}
          onClick={() => onReplace(field)}
        >
          Replace value
        </button>
      )}
    </span>
  );
};

interface ReplaceRowProps {
  field: string;
  text: string;
  busy: boolean;
  onText: (text: string) => void;
  onSave: () => void;
  onCancel: () => void;
}

// ReplaceRow takes a new plain value for an encrypted field. ndb encrypts
// it on the write.
const ReplaceRow: FC<ReplaceRowProps> = ({ field, text, busy, onText, onSave, onCancel }) => {
  const keys = (event: KeyboardEvent) => {
    if (event.key === 'Enter') onSave();

    if (event.key === 'Escape') onCancel();
  };

  return (
    <div class="seal-replace">
      <span class="tkey">{field}</span>

      <input
        class="tinput"
        type="text"
        autocomplete="off"
        data-1p-ignore
        data-lpignore="true"
        placeholder="New value (JSON or text)"
        aria-label={`New value of ${field}`}
        value={text}
        onInput={(event: Event) => onText((event.target as HTMLInputElement).value)}
        onKeyDown={keys}
      />

      <button type="button" class="tbtn" disabled={busy || text === ''} onClick={onSave}>
        Save
      </button>

      <button type="button" class="tbtn" onClick={onCancel}>
        Cancel
      </button>
    </div>
  );
};

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
  const { coll, id, doc: loaded, saveUrl, lockUrl, unlockUrl, deleteUrl, find, lockedUntil, writable, reason, showUrl = '' } = props;

  const [mode, setMode] = useState<Mode>('tree');
  const [doc, setDoc] = useState<Record<string, unknown>>(loaded);
  const [draft, setDraft] = useState<Record<string, unknown>>(doc);
  const [text, setText] = useState(pretty(doc));
  const [notice, setNotice] = useState<Notice | null>(startNotice(props.notice, props.problem));
  const [saving, setSaving] = useState(false);
  const [locked, setLocked] = useState<string | null>(lockedUntil ?? null);
  const [locking, setLocking] = useState(false);
  const [sealed, setSealed] = useState<string[]>(props.sealed ?? []);
  const [shown, setShown] = useState<Shown | null>(null);
  const [showing, setShowing] = useState(false);
  const [replacing, setReplacing] = useState<string | null>(null);
  const [replaceText, setReplaceText] = useState('');

  const send = useSend(saveUrl);
  const sendLock = useSend(lockUrl);
  const sendUnlock = useSend(unlockUrl);
  const sendShow = useSend(showUrl);

  useEffect(() => {
    if (!shown) return;

    const timer = setTimeout(() => setShown(null), SHOW_SECONDS * 1000);

    return () => clearTimeout(timer);
  }, [shown]);

  useEffect(() => {
    const drawer = document.getElementById('doc-editor')?.closest('dialog');
    const hide = () => setShown(null);

    drawer?.addEventListener('close', hide);

    return () => drawer?.removeEventListener('close', hide);
  }, []);

  useEffect(() => {
    if (!locked) return;

    const left = new Date(locked).getTime() - Date.now();
    const timer = setTimeout(() => setLocked(null), Math.max(0, left));

    return () => clearTimeout(timer);
  }, [locked]);

  const version = Number(doc._v ?? 0);
  const parsed = mode === 'json' ? parseObject(text) : draft;
  const current = typeof parsed === 'string' ? draft : parsed;
  const diff = keepSealed(diffDocs(doc, current), sealed);
  const sealedSet = new Set(sealed);
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

  const write = async (set: Record<string, unknown>, unset: string[]): Promise<boolean> => {
    setSaving(true);

    const answer = await send<{ message?: string; conflict?: boolean; lockEnded?: boolean; doc?: Record<string, unknown> }>({ coll, id, _v: version, set, unset });

    setSaving(false);

    if (answer.ok && answer.body.doc) {
      const masked = maskDoc(answer.body.doc);
      const saved = { _id: masked.doc._id, _v: masked.doc._v, ...masked.doc };

      setSealed(masked.sealed);
      setShown(null);
      setDoc(saved);
      setDraft(saved);
      setText(pretty(saved));
      setNotice({ tone: 'ok', text: `Saved. Now _v ${String(saved._v)}.` });
      document.dispatchEvent(new CustomEvent(DOC_SAVED, { detail: { id } }));

      return true;
    }

    if (answer.body.conflict) {
      setNotice({ tone: 'bad', text: 'Somebody else changed this document; reload.', reload: true });

      return false;
    }

    if (answer.body.lockEnded) setLocked(null);

    setNotice({ tone: 'bad', text: answer.body.message ?? 'The save failed.' });

    return false;
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

    await write(diff.set, diff.unset);
  };

  const show = async (field: string) => {
    setShowing(true);

    const answer = await sendShow<{ value?: unknown; denied?: boolean; message?: string }>({ coll, id, field });

    setShowing(false);

    if (answer.ok && 'value' in answer.body) {
      setShown({ field, text: plainText(answer.body.value) });

      return;
    }

    setNotice({ tone: 'bad', text: answer.body.denied ? deniedText(coll) : (answer.body.message ?? 'The value could not be read.') });
  };

  const copyShown = async (value: string) => {
    const copied = await copyText(value);

    setNotice({ tone: copied ? 'ok' : 'bad', text: copied ? 'Copied the value.' : 'The browser blocked the clipboard.' });
  };

  const startReplace = (field: string) => {
    setShown(null);
    setReplaceText('');
    setReplacing(field);
  };

  const saveReplace = async () => {
    if (!replacing || replaceText === '') return;

    const done = await write({ [replacing]: parseScalar(replaceText) }, []);

    if (done) setReplacing(null);
  };

  const sealSlot = (row: Row) => (
    <SealActions
      row={row}
      shown={shown}
      writable={writable}
      busy={showing}
      onShow={show}
      onHide={() => setShown(null)}
      onCopy={copyShown}
      onReplace={startReplace}
    />
  );

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
            sealed={sealedSet}
            sealedSlot={sealSlot}
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

        {replacing && (
          <ReplaceRow
            field={replacing}
            text={replaceText}
            busy={saving}
            onText={setReplaceText}
            onSave={saveReplace}
            onCancel={() => setReplacing(null)}
          />
        )}

        {mode === 'json' && sealed.length > 0 && <p class="hint seal-note">{JSON_NOTE}</p>}

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
