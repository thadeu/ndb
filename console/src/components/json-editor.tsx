import type { FC } from 'hono/jsx';
import { useEffect, useRef, useState } from 'hono/jsx';

import { lineAt } from '@/components/devtools-blocks';
import { formatJson, jsonMark, type Mark, type Snap } from '@/components/devtools-model';
import { editKey } from '@/components/editor-keys';
import { FindBar, Highlight, IconButton, type KeyHint, KeysButton, LINE_HEIGHT, Matches } from '@/components/editor-parts';
import { useEditHistory } from '@/hooks/useEditHistory';
import { useFind } from '@/hooks/useFind';
import { noSpelling } from '@/ui/input';

export interface JsonEditorProps {
  value: string;
  onChange: (text: string) => void;
  readOnly?: boolean;
  // onSubmit runs on Ctrl or Cmd + Enter.
  onSubmit?: () => void;
  label?: string;
}

const KEYS: KeyHint[] = [
  ['Ctrl Enter', 'Save'],
  ['Ctrl F', 'Find'],
  ['Ctrl Z', 'Undo'],
  ['Ctrl Shift Z', 'Redo'],
  ['Tab', 'Indent'],
  ['Shift Tab', 'Unindent'],
];

const JsonGutter: FC<{ count: number; line: number; mark?: Mark }> = ({ count, line, mark }) => (
  <div class="ed-gutter jed-gutter">
    {Array.from({ length: count }, (_, index) => (
      <div class={index === line ? 'ed-ln on' : 'ed-ln'}>
        {mark?.line === index && (
          <span
            class="ed-err"
            role="img"
            title={mark.message}
            aria-label={mark.message}
          />
        )}

        <span aria-hidden="true">{index + 1}</span>
      </div>
    ))}
  </div>
);

// JsonEditor is the dark editor of Dev Tools for one JSON value: line
// numbers, colors, Tab indent, closing pairs, undo and redo, Format, find,
// and a red mark on the line of a parse error.
export const JsonEditor: FC<JsonEditorProps> = ({ value, onChange, readOnly = false, onSubmit, label = 'JSON' }) => {
  const [line, setLine] = useState(0);

  const area = useRef<HTMLTextAreaElement | null>(null);
  const scroller = useRef<HTMLDivElement | null>(null);

  const show = (snap: Snap) => {
    onChange(snap.text);
    setLine(lineAt(snap.text, snap.start));
  };

  const edits = useEditHistory(area, value, show);
  const find = useFind(area, scroller, value, LINE_HEIGHT);

  useEffect(() => edits.adopt(value), [value]);

  const lines = value.split('\n');
  const mark = jsonMark(value);
  const marks = mark ? [mark] : [];
  const height = `height:${lines.length * LINE_HEIGHT + 28}px`;

  const track = () => {
    const element = area.current;

    if (!element) return;

    edits.track();
    setLine(lineAt(element.value, element.selectionStart));
  };

  const format = () => {
    const formatted = formatJson(value);

    if (formatted === undefined || formatted === value) return;

    change({ text: formatted, start: 0, end: 0 });
  };

  const change = (snap: Snap) => {
    if (!readOnly) edits.change(snap);
  };

  const keys = (event: KeyboardEvent) => {
    const element = area.current;
    const command = event.metaKey || event.ctrlKey;
    const key = event.key.toLowerCase();

    if (command && (key === 'z' || (key === 'y' && event.ctrlKey))) {
      event.preventDefault();

      if (!readOnly) edits.step(key === 'z' && !event.shiftKey);

      return;
    }

    if (command && key === 'f') {
      event.preventDefault();
      find.show();

      return;
    }

    if (command && event.key === 'Enter') {
      event.preventDefault();
      onSubmit?.();

      return;
    }

    if (!element || readOnly || command || event.altKey || event.isComposing) return;

    const edit = editKey(element.value, element.selectionStart, element.selectionEnd, { key: event.key, shift: event.shiftKey });

    if (!edit) return;

    event.preventDefault();
    change(edit);
  };

  const typed = (event: Event) => {
    const element = event.target as HTMLTextAreaElement;

    edits.typed(event);
    onChange(element.value);
    setLine(lineAt(element.value, element.selectionStart));
  };

  return (
    <section class="ed jed" aria-label={label}>
      <div class="ed-head">
        <span class="ed-title">{label}</span>

        <IconButton
          icon="undo"
          label="Undo (Ctrl or Cmd + Z)"
          disabled={readOnly || !edits.canUndo}
          onClick={() => edits.step(true)}
        />

        <IconButton
          icon="redo"
          label="Redo (Ctrl or Cmd + Shift + Z)"
          disabled={readOnly || !edits.canRedo}
          onClick={() => edits.step(false)}
        />

        <IconButton icon="search" label="Find (Ctrl or Cmd + F)" onClick={find.show} />
        <KeysButton items={KEYS} />

        <span class="grow" />

        <button
          type="button"
          class="ed-btn"
          disabled={readOnly || mark !== undefined}
          onClick={format}
        >
          Format
        </button>
      </div>

      {find.open && (
        <FindBar
          query={find.query}
          count={find.found.length}
          current={find.current}
          label="Find in the JSON"
          onQuery={find.search}
          onMove={find.move}
          onClose={find.close}
        />
      )}

      <div class="ed-scroll" ref={scroller}>
        <JsonGutter count={lines.length} line={line} mark={mark} />

        <div class="ed-main">
          <Highlight lines={lines} on={{ start: line, end: line }} marks={marks} />

          {find.found.length > 0 && <Matches text={value} found={find.found} size={find.query.length} current={find.current} />}

          <textarea
            ref={area}
            class="ed-ta"
            style={height}
            value={value}
            {...noSpelling}
            wrap="off"
            aria-label={label}
            aria-invalid={mark !== undefined}
            readOnly={readOnly}
            onInput={typed}
            onKeyDown={keys}
            onKeyUp={track}
            onClick={track}
            onFocus={track}
          />
        </div>
      </div>

      {mark && (
        <p class="jed-error" role="status">
          Line {mark.line + 1}. {mark.message}
        </p>
      )}
    </section>
  );
};
