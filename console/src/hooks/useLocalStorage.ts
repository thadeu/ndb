import { useState } from 'hono/jsx';

function read<T>(key: string, initial: T): T {
  try {
    const text = window.localStorage.getItem(key);

    return text === null ? initial : (JSON.parse(text) as T);
  } catch {
    return initial;
  }
}

// useLocalStorage keeps a JSON value in the browser under key. Storage can be
// blocked or full, so every access is guarded: the value then lives only in
// memory and the page still works.
export function useLocalStorage<T>(key: string, initial: T): [T, (next: T) => void] {
  const [value, setValue] = useState<T>(() => read(key, initial));

  const save = (next: T) => {
    setValue(next);

    try {
      window.localStorage.setItem(key, JSON.stringify(next));
    } catch {
      return;
    }
  };

  return [value, save];
}
