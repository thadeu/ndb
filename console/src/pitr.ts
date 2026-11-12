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

const UNIT_MS: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };

// windowMs reads a window as ndb writes it (1d, 6h, 90m). Another text
// gives undefined.
export function windowMs(text: string): number | undefined {
  const match = /^(\d+)([smhd])$/.exec(text.trim());

  return match ? Number(match[1]) * (UNIT_MS[match[2] as string] as number) : undefined;
}

// barLeft is where the restorable part of the window bar starts, in percent.
// The bar is the whole window, up to now. The part from oldest to now can be
// restored. With no oldest instant, nothing can (100).
export function barLeft(window: string, oldest: string | undefined, now = Date.now()): number {
  const size = windowMs(window);
  const start = oldest ? Date.parse(oldest) : Number.NaN;

  if (Number.isNaN(start)) return 100;

  if (!size) return 0;

  return Math.round(Math.min(100, Math.max(0, ((start - (now - size)) / size) * 100)));
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// shortWhen shows an RFC 3339 time as "Nov 10, 20:19", in UTC.
export function shortWhen(value?: string): string {
  const date = value ? new Date(value) : undefined;

  if (!date || Number.isNaN(date.getTime())) return '—';

  return `${MONTHS[date.getUTCMonth()]} ${date.getUTCDate()}, ${date.toISOString().slice(11, 16)}`;
}

// seconds is a time of the date picker (UTC) in unix seconds, for the range
// slider of the restore card. A text that is not a time gives undefined.
export function seconds(value: string): number | undefined {
  const time = Date.parse(utc(value));

  return Number.isNaN(time) ? undefined : Math.floor(time / 1000);
}

// sliderRange is the range of the slider: from the first second that can be
// restored to now.
export function sliderRange(oldest: string, now = Date.now()): { min: number; max: number } {
  const max = Math.floor(now / 1000);

  return { min: Math.min(max, Math.ceil(Date.parse(oldest) / 1000) + 1), max };
}

// restoreName is the name of the copy that a restore in place makes before
// the rename: the collection name (cut so the result fits 64 characters) and
// the time in base 36.
export function restoreName(collection: string, now = Date.now()): string {
  return `${collection.slice(0, 48)}_r${Math.floor(now / 1000).toString(36)}`;
}
