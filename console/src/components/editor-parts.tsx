import type { FC } from 'hono/jsx';
import { useEffect, useRef, useState } from 'hono/jsx';

import { lineAt, tokenize } from '@/components/devtools-blocks';
import { type Mark, offsetOfLine } from '@/components/devtools-model';
import { useOutsideClick } from '@/hooks/useOutsideClick';
import { noSpelling } from '@/ui/input';

// The parts of the dark code editor that Dev Tools and the JSON editor
// share: icons, the colored layer, the find bar and its matches.

export const LINE_HEIGHT = 22;

const ICONS = {
  play: 'M8 5v14l11-7z',
  copy: 'M9 9h10v10H9zM5 15V5h10',
  undo: 'M9 14L4 9l5-5M4 9h10a6 6 0 010 12h-3',
  redo: 'M15 14l5-5-5-5M20 9H10a6 6 0 000 12h3',
  expand: 'M7 10l5 5 5-5',
  collapse: 'M7 14l5-5 5 5',
  up: 'M6 15l6-6 6 6',
  down: 'M6 9l6 6 6-6',
  close: 'M6 6l12 12M18 6L6 18',
  search: 'M11 5a6 6 0 100 12 6 6 0 000-12zM20 20l-4-4',
  keys: 'M3 6h18v12H3zM7 10h.01M11 10h.01M15 10h.01M8 14h8',
};

export type IconName = keyof typeof ICONS;

export const Svg: FC<{ name: IconName; size?: number }> = ({ name, size = 14 }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill={name === 'play' ? 'currentColor' : 'none'}
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d={ICONS[name]} />
  </svg>
);

interface IconButtonProps {
  icon: IconName;
  label: string;
  onClick: () => void;
  disabled?: boolean;
  tone?: string;
}

export const IconButton: FC<IconButtonProps> = ({ icon, label, onClick, disabled, tone = 'ed-icon' }) => (
  <button
    type="button"
    class={tone}
    title={label}
    aria-label={label}
    disabled={disabled}
    onMouseDown={(event: MouseEvent) => event.preventDefault()}
    onClick={onClick}
  >
    <Svg name={icon} />
  </button>
);

interface HighlightProps {
  lines: string[];
  on?: { start: number; end: number };
  marks: Mark[];
}

// Highlight is the colored layer under the textarea. The lines from on.start
// to on.end get a background; a line with a mark gets a red wave.
export const Highlight: FC<HighlightProps> = ({ lines, on, marks }) => {
  const errors = new Set(marks.map(mark => mark.line));

  return (
    <pre class="ed-hl" aria-hidden="true">
      {lines.map((line, index) => {
        const inside = on !== undefined && index >= on.start && index <= on.end;
        const tone = `ed-line${inside ? ' on' : ''}${errors.has(index) ? ' err' : ''}`;

        return <div class={tone}>{line === '' ? '​' : tokenize(line).map(token => <span class={`tok-${token.tone}`}>{token.text}</span>)}</div>;
      })}
    </pre>
  );
};

// position is the line and the column of an offset, for the boxes drawn
// over the editor (find matches, the autocomplete list).
export function position(text: string, offset: number): { line: number; column: number } {
  const line = lineAt(text, offset);

  return { line, column: offset - offsetOfLine(text, line) };
}

export const Matches: FC<{ text: string; found: number[]; size: number; current: number }> = ({ text, found, size, current }) => (
  <div class="ed-matches" aria-hidden="true">
    {found.map((offset, index) => {
      const at = position(text, offset);
      const style = `top:${at.line * LINE_HEIGHT}px;left:calc(16px + ${at.column}ch);width:${size}ch`;

      return (
        <span
          class={index === current ? 'ed-match on' : 'ed-match'}
          style={style}
        />
      );
    })}
  </div>
);

interface FindBarProps {
  query: string;
  count: number;
  current: number;
  label?: string;
  onQuery: (text: string) => void;
  onMove: (step: number) => void;
  onClose: () => void;
}

export const FindBar: FC<FindBarProps> = ({ query, count, current, label = 'Find in the requests', onQuery, onMove, onClose }) => {
  const input = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);

  const status = count === 0 ? (query ? 'No match' : '') : `${current + 1} of ${count}`;

  const keys = (event: KeyboardEvent) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      onMove(event.shiftKey ? -1 : 1);
    }

    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
    }
  };

  return (
    <div class="ed-find" role="search">
      <input
        ref={input}
        class="ed-find-input"
        type="search"
        value={query}
        placeholder="Find"
        aria-label={label}
        {...noSpelling}
        onInput={(event: Event) => onQuery((event.target as HTMLInputElement).value)}
        onKeyDown={keys}
      />

      <span
        class="ed-find-count"
        role="status"
      >
        {status}
      </span>

      <IconButton icon="up" label="Previous match" disabled={count === 0} onClick={() => onMove(-1)} />
      <IconButton icon="down" label="Next match" disabled={count === 0} onClick={() => onMove(1)} />
      <IconButton icon="close" label="Close find" onClick={onClose} />
    </div>
  );
};

export type KeyHint = [keys: string, what: string];

// KeysButton shows the keyboard shortcuts of the editor in a small list, so
// the editor needs no footer of hints.
export const KeysButton: FC<{ items: KeyHint[] }> = ({ items }) => {
  const [open, setOpen] = useState(false);
  const host = useRef<HTMLDivElement | null>(null);

  useOutsideClick(host, open, () => setOpen(false));

  return (
    <div class="pop-host" ref={host}>
      <IconButton icon="keys" label="Keys" onClick={() => setOpen(!open)} />

      {open && (
        <dl class="popover ed-keys" aria-label="Keys">
          {items.map(([keys, what]) => (
            <div class="ed-keys-row">
              <dt class="mono">{keys}</dt>
              <dd>{what}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
};
