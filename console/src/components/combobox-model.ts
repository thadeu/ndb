// Pure logic of the combobox (src/components/ui/combobox.tsx): which options
// a text keeps, the row for a free value, and the last item of a comma list.

export interface ComboOption {
  id: string;
  // hint is a short text on the right of the row, such as a document count.
  hint?: string;
  badge?: string;
}

export interface Parts {
  // head is the text up to the last comma, with the comma. part is the item
  // that the user is typing.
  head: string;
  part: string;
}

// splitList cuts a comma list at its last item: "a,b" is head "a," and part
// "b". Without `multiple` the whole text is the part.
export function splitList(text: string, multiple: boolean): Parts {
  const at = multiple ? text.lastIndexOf(',') : -1;

  return { head: text.slice(0, at + 1), part: text.slice(at + 1).trim() };
}

// filterOptions keeps the options that have the text, without case. The ones
// that start with the text come first. An empty text keeps every option.
export function filterOptions(options: ComboOption[], text: string): ComboOption[] {
  const needle = text.trim().toLowerCase();

  if (needle === '') return options;

  const starts = options.filter(option => option.id.toLowerCase().startsWith(needle));
  const holds = options.filter(option => !option.id.toLowerCase().startsWith(needle) && option.id.toLowerCase().includes(needle));

  return [...starts, ...holds];
}

// customValue is the text of the last row, "Use "<text>"": the typed text
// when it is not an option.
export function customValue(options: ComboOption[], text: string): string | undefined {
  const value = text.trim();

  if (value === '' || options.some(option => option.id === value)) return undefined;

  return value;
}

// pick puts an option in the place of the item that the user is typing.
export function pick(text: string, id: string, multiple: boolean): string {
  return splitList(text, multiple).head + id;
}

// step moves the active row by one, and goes around at each end. -1 is no
// active row.
export function step(active: number, count: number, delta: 1 | -1): number {
  if (count === 0) return -1;

  if (active === -1) return delta === 1 ? 0 : count - 1;

  return (active + delta + count) % count;
}
