import type { RefObject } from 'hono/jsx';
import { useEffect } from 'hono/jsx';

// useOutsideClick calls onOutside when a mouse press starts outside the
// element. It listens only while active is true.
export function useOutsideClick(ref: RefObject<HTMLElement | null>, active: boolean, onOutside: () => void): void {
  useEffect(() => {
    if (!active) return;

    const listener = (event: MouseEvent) => {
      if (!ref.current?.contains(event.target as Node)) onOutside();
    };

    document.addEventListener('mousedown', listener);

    return () => document.removeEventListener('mousedown', listener);
  }, [active]);
}
