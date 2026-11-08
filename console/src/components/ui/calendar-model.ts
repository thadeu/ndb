// The model of the date picker calendar: the days of a month view, the
// presets ("5 minutes ago", "Last 7 days") and the range selection. Days are
// UTC dates written YYYY-MM-DD; they compare as text.

export interface Day {
  iso: string;
  day: number;
  inMonth: boolean;
  today: boolean;
}

// Preset is one quick choice. ago picks an instant that long ago (single
// mode); last picks the range from that long ago until now (range mode); at
// picks the min or max of the picker.
export interface Preset {
  label: string;
  ago?: string;
  last?: string;
  at?: 'min' | 'max';
}

export const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

export const WEEKDAYS = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'];

export function isoDay(date: Date): string {
  return date.toISOString().slice(0, 10);
}

// monthGrid is the 6 weeks (42 days) that show a month, Monday first.
export function monthGrid(year: number, month: number, today: string): Day[] {
  const first = new Date(Date.UTC(year, month, 1));
  const offset = (first.getUTCDay() + 6) % 7;

  return Array.from({ length: 42 }, (_, i) => {
    const date = new Date(Date.UTC(year, month, 1 - offset + i));
    const iso = isoDay(date);

    return { iso, day: date.getUTCDate(), inMonth: date.getUTCMonth() === month, today: iso === today };
  });
}

export function addMonths(year: number, month: number, delta: number): { year: number; month: number } {
  const date = new Date(Date.UTC(year, month + delta, 1));

  return { year: date.getUTCFullYear(), month: date.getUTCMonth() };
}

export function addDays(iso: string, delta: number): string {
  const date = new Date(`${iso}T00:00:00Z`);

  date.setUTCDate(date.getUTCDate() + delta);

  return isoDay(date);
}

// outside tells if a day is before the day of min or after the day of max.
export function outside(iso: string, min?: Date, max?: Date): boolean {
  return (min !== undefined && iso < isoDay(min)) || (max !== undefined && iso > isoDay(max));
}

export function clampDate(date: Date, min?: Date, max?: Date): Date {
  if (min && date < min) return new Date(min);
  if (max && date > max) return new Date(max);

  return date;
}

const UNITS: Record<string, number> = { s: 1_000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000, mo: 2_592_000_000, y: 31_536_000_000 };

// duration reads 30s, 5m, 1h, 7d, 2w, 1mo (30 days), 1y in milliseconds; 0
// when it is not one.
export function duration(text: string): number {
  const match = /^(\d+)(mo|[smhdwy])$/.exec(text.trim());

  return match ? Number(match[1]) * (UNITS[match[2] ?? ''] ?? 0) : 0;
}

// resolvePreset gives the instant (single) or the range of a preset now.
export function resolvePreset(preset: Preset, now: Date, min?: Date, max?: Date): { at?: Date; from?: Date; to?: Date } {
  if (preset.at) return { at: preset.at === 'min' ? min : max };

  if (preset.last) return { from: clampDate(new Date(now.getTime() - duration(preset.last)), min, max), to: clampDate(now, min, max) };

  return { at: clampDate(new Date(now.getTime() - duration(preset.ago ?? '0s')), min, max) };
}

// pickRange is a click on a day in range mode: the first click starts a new
// range, the second ends it (in either order).
export function pickRange(range: { from?: string; to?: string }, iso: string): { from: string; to?: string } {
  if (!range.from || range.to) return { from: iso };

  return iso < range.from ? { from: iso, to: range.from } : { from: range.from, to: iso };
}

export function inRange(iso: string, from?: string, to?: string): boolean {
  return from !== undefined && to !== undefined && iso >= from && iso <= to;
}
