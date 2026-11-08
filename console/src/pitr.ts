// Pure helpers of the PITR block of the Backups tab.

const DEFAULT_AGO_MS = 5 * 60_000;

export const PITR_PAGE_SIZE = 10;

// atValue is a time in the shape of the date picker, YYYY-MM-DD HH:MM:SS, in
// UTC.
export function atValue(date: Date): string {
  return date.toISOString().slice(0, 19).replace('T', ' ');
}

// defaultAt is a few minutes ago, but not before the window starts.
export function defaultAt(oldest?: string, now = Date.now()): string {
  const start = oldest ? Date.parse(oldest) + 1000 : 0;

  return atValue(new Date(Math.min(now, Math.max(now - DEFAULT_AGO_MS, start))));
}

// utc reads a time of the date picker (or of a datetime-local input) as UTC,
// in RFC 3339.
export function utc(value: string): string {
  const text = value.trim().replace(' ', 'T');

  if (/[zZ]|[+-]\d\d:\d\d$/.test(text)) return text;

  return `${text.length === 16 ? `${text}:00` : text}Z`;
}

// pageOf cuts a list into pages of size and returns page n (from 1, kept in
// range) and the number of pages.
export function pageOf<T>(items: T[], n: number, size = PITR_PAGE_SIZE): { rows: T[]; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(items.length / size));
  const page = Math.min(Math.max(1, Number.isFinite(n) ? Math.trunc(n) : 1), pages);

  return { rows: items.slice((page - 1) * size, page * size), page, pages };
}

// when shows an RFC 3339 time to the second, in UTC.
export function when(value?: string): string {
  return value ? `${value.slice(0, 19).replace('T', ' ')} UTC` : '—';
}
