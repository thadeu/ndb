import type { RefObject } from 'hono/jsx';
import { useRef, useState } from 'hono/jsx';

import { emptyHistory, type History, record, redo, type Snap, undo } from '@/components/devtools-model';

export function snapOf(element: HTMLTextAreaElement): Snap {
  return { text: element.value, start: element.selectionStart, end: element.selectionEnd };
}

export interface EditHistory {
  canUndo: boolean;
  canRedo: boolean;
  // change is every programmatic edit: it keeps the state before it, so undo
  // restores both the text and the cursor.
  change: (snap: Snap) => void;
  // typed records a native input event of the textarea.
  typed: (event: Event) => void;
  // track keeps the cursor after a click or a key that moves it.
  track: () => void;
  step: (back: boolean) => boolean;
  // adopt takes a text that changed outside the editor, such as Discard.
  adopt: (text: string) => void;
}

// useEditHistory is the undo and redo stack of a code textarea. show puts a
// state on the screen (the text state of the owner, the current line).
export function useEditHistory(area: RefObject<HTMLTextAreaElement | null>, initial: string, show: (snap: Snap) => void): EditHistory {
  const [undos, setUndos] = useState<History>(emptyHistory());
  const last = useRef<Snap>({ text: initial, start: 0, end: 0 });

  const put = (snap: Snap) => {
    const element = area.current;

    if (element) {
      element.value = snap.text;
      element.setSelectionRange(snap.start, snap.end);
    }

    last.current = snap;

    show(snap);
  };

  const change = (snap: Snap) => {
    setUndos(record(undos, { before: last.current, now: Date.now() }));
    put(snap);
  };

  const typed = (event: Event) => {
    const element = event.target as HTMLTextAreaElement;
    const input = event as InputEvent;
    const plain = input.inputType === 'insertText' || input.inputType?.startsWith('delete');

    setUndos(record(undos, { before: last.current, typed: plain ? (input.data ?? '') : undefined, now: Date.now() }));

    last.current = snapOf(element);
  };

  const track = () => {
    if (area.current) last.current = snapOf(area.current);
  };

  const step = (back: boolean) => {
    const element = area.current;
    const now = element ? snapOf(element) : last.current;
    const moved = back ? undo(undos, now) : redo(undos, now);

    if (!moved) return false;

    setUndos(moved.history);
    put(moved.snap);
    area.current?.focus();

    return true;
  };

  const adopt = (text: string) => {
    if (text === last.current.text) return;

    setUndos(record(undos, { before: last.current, now: Date.now() }));

    last.current = { text, start: 0, end: 0 };
  };

  return { canUndo: undos.past.length > 0, canRedo: undos.future.length > 0, change, typed, track, step, adopt };
}
