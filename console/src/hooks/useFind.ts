import type { RefObject } from 'hono/jsx';
import { useState } from 'hono/jsx';

import { lineAt } from '@/components/devtools-blocks';
import { findAll } from '@/components/devtools-model';

export interface Find {
  open: boolean;
  query: string;
  found: number[];
  current: number;
  show: () => void;
  search: (query: string) => void;
  move: (offset: number) => void;
  close: () => void;
}

// useFind is the find bar of a code textarea: the matches of the query, the
// current one, and the scroll to it. lineHeight is the height of one line.
export function useFind(area: RefObject<HTMLTextAreaElement | null>, scroller: RefObject<HTMLElement | null>, text: string, lineHeight: number): Find {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [match, setMatch] = useState(0);

  const found = open ? findAll(text, query) : [];
  const current = found.length === 0 ? 0 : Math.min(match, found.length - 1);

  const move = (offset: number) => {
    if (found.length === 0) return;

    const next = (current + offset + found.length) % found.length;
    const at = found[next] as number;

    setMatch(next);
    area.current?.setSelectionRange(at, at + query.length);

    if (scroller.current) scroller.current.scrollTop = Math.max(0, (lineAt(text, at) - 3) * lineHeight);
  };

  const close = () => {
    const at = found[current];

    setOpen(false);
    area.current?.focus();

    if (at !== undefined) area.current?.setSelectionRange(at, at + query.length);
  };

  const search = (next: string) => {
    setQuery(next);
    setMatch(0);
  };

  return { open, query, found, current, show: () => setOpen(true), search, move, close };
}
