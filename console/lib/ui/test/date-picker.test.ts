import { describe, expect, it } from 'bun:test';

import { addDays, addMonths, duration, inRange, monthGrid, outside, pickRange, resolvePreset } from '../src/calendar-model';
import { erase, format, normalize, parse, step, typeDigit, type MaskKind, type MaskState } from '../src/date-mask';
import { place } from '../src/popover-position';

function typed(text: string, kind: MaskKind = 'datetime', start: MaskState = { value: '', caret: 0 }): MaskState {
  let state = start;

  for (const ch of text) state = typeDigit(state, ch, kind);

  return state;
}

describe('date mask', () => {
  it('writes the separators as the digits come', () => {
    expect(typed('20261112140405')).toEqual({ value: '2026-11-12 14:04:05', caret: 19 });
    expect(typed('2026111', 'date')).toEqual({ value: '2026-11-1', caret: 9 });
    expect(typed('202611')).toEqual({ value: '2026-11-', caret: 8 });
  });

  it('puts a 0 before a digit that cannot start the part', () => {
    expect(typed('20264').value).toBe('2026-04-');
    expect(typed('2026045').value).toBe('2026-04-05 ');
    expect(typed('202604059').value).toBe('2026-04-05 09:');
    expect(typed('20260405096').value).toBe('2026-04-05 09:06:');
  });

  it('keeps complete parts in range, the day by the month', () => {
    expect(typed('202613').value).toBe('2026-12-');
    expect(typed('202600').value).toBe('2026-01-');
    expect(typed('20260231').value).toBe('2026-02-28 ');
    expect(typed('20240231').value).toBe('2024-02-29 ');
    expect(typed('2026010125').value).toBe('2026-01-01 23:');
  });

  it('overwrites in the middle and keeps the other places', () => {
    const full = typed('20261112140405');

    expect(typeDigit({ value: full.value, caret: 5 }, '0', 'datetime')).toEqual({ value: '2026-01-12 14:04:05', caret: 6 });
    expect(typeDigit({ value: full.value, caret: 4 }, '0', 'datetime').value).toBe('2026-01-12 14:04:05');
    expect(typeDigit(full, '9', 'datetime')).toEqual(full);
  });

  it('erases the last digit at the end, and zeroes a digit in the middle', () => {
    expect(erase({ value: '2026-11-', caret: 8 }, 'datetime')).toEqual({ value: '2026-1', caret: 6 });
    expect(erase({ value: '2026-11-12', caret: 6 }, 'date')).toEqual({ value: '2026-01-12', caret: 5 });
    expect(erase({ value: '2026-11-12', caret: 5 }, 'date')).toEqual({ value: '2020-11-12', caret: 3 });
    expect(erase({ value: '2026-11-12', caret: 9 }, 'date', true)).toEqual({ value: '2026-11-1', caret: 9 });
    expect(erase({ value: '', caret: 0 }, 'date')).toEqual({ value: '', caret: 0 });
  });

  it('steps the part at the caret with wrap', () => {
    expect(step({ value: '2026-12-31', caret: 6 }, 1, 'date').value).toBe('2026-01-31');
    expect(step({ value: '2026-03-31', caret: 6 }, -1, 'date').value).toBe('2026-02-28');
    expect(step({ value: '2026-11-12 23:00:00', caret: 13 }, 1, 'datetime').value).toBe('2026-11-12 00:00:00');
    expect(step({ value: '2026-1', caret: 6 }, 1, 'date').value).toBe('2026-1');
  });

  it('reads pasted text', () => {
    expect(normalize('2026-11-12T14:04:05.123Z', 'datetime')).toBe('2026-11-12 14:04:05');
    expect(normalize('2026-1-5 9:4', 'datetime')).toBe('2026-01-05 09:04');
    expect(normalize('20261112', 'date')).toBe('2026-11-12');
    expect(normalize('nope', 'date')).toBe('');
  });

  it('parses and formats in UTC', () => {
    expect(parse('2026-11-12 14:04:05', 'datetime')?.toISOString()).toBe('2026-11-12T14:04:05.000Z');
    expect(parse('2026-11-12', 'date')?.toISOString()).toBe('2026-11-12T00:00:00.000Z');
    expect(parse('2026-11-1', 'date')).toBeNull();
    expect(format(new Date('2026-11-12T14:04:05Z'), 'datetime')).toBe('2026-11-12 14:04:05');
  });
});

describe('calendar model', () => {
  it('shows 6 weeks from the Monday before the 1st', () => {
    const days = monthGrid(2026, 10, '2026-11-08');

    expect(days).toHaveLength(42);
    expect(days[0]?.iso).toBe('2026-10-26');
    expect(days.find(d => d.today)?.iso).toBe('2026-11-08');
    expect(days.filter(d => d.inMonth)).toHaveLength(30);
  });

  it('moves by months and days across years', () => {
    expect(addMonths(2026, 11, 1)).toEqual({ year: 2027, month: 0 });
    expect(addMonths(2026, 0, -1)).toEqual({ year: 2025, month: 11 });
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01');
  });

  it('knows the days out of min and max', () => {
    const min = new Date('2026-11-07T14:00:00Z');
    const max = new Date('2026-11-08T10:00:00Z');

    expect(outside('2026-11-06', min, max)).toBe(true);
    expect(outside('2026-11-07', min, max)).toBe(false);
    expect(outside('2026-11-09', min, max)).toBe(true);
  });

  it('resolves presets inside min and max', () => {
    const now = new Date('2026-11-08T12:00:00Z');
    const min = new Date('2026-11-08T11:00:00Z');

    expect(duration('7d')).toBe(7 * 86_400_000);
    expect(duration('1mo')).toBe(30 * 86_400_000);
    expect(duration('x')).toBe(0);
    expect(resolvePreset({ label: '15m', ago: '15m' }, now).at?.toISOString()).toBe('2026-11-08T11:45:00.000Z');
    expect(resolvePreset({ label: '6h', ago: '6h' }, now, min).at?.toISOString()).toBe(min.toISOString());
    expect(resolvePreset({ label: 'start', at: 'min' }, now, min).at).toBe(min);
    expect(resolvePreset({ label: '7d', last: '7d' }, now).from?.toISOString()).toBe('2026-11-01T12:00:00.000Z');
  });

  it('picks a range in either order', () => {
    expect(pickRange({}, '2026-11-05')).toEqual({ from: '2026-11-05' });
    expect(pickRange({ from: '2026-11-05' }, '2026-11-02')).toEqual({ from: '2026-11-02', to: '2026-11-05' });
    expect(pickRange({ from: '2026-11-05' }, '2026-11-09')).toEqual({ from: '2026-11-05', to: '2026-11-09' });
    expect(pickRange({ from: '2026-11-02', to: '2026-11-05' }, '2026-11-20')).toEqual({ from: '2026-11-20' });
    expect(inRange('2026-11-03', '2026-11-02', '2026-11-05')).toBe(true);
  });
});

describe('popover position', () => {
  const viewport = { width: 1000, height: 800 };

  it('goes below, on the start edge, when it fits', () => {
    expect(place({ top: 100, left: 50, right: 250, bottom: 140 }, { width: 300, height: 200 }, viewport)).toEqual({ top: 146, left: 50, maxHeight: 646, placement: 'bottom-start' });
  });

  it('flips above when there is more room there', () => {
    const placed = place({ top: 700, left: 50, right: 250, bottom: 740 }, { width: 300, height: 300 }, viewport);

    expect(placed.placement).toBe('top-start');
    expect(placed.top).toBe(394);
  });

  it('moves back inside the viewport, and aligns end on end', () => {
    expect(place({ top: 100, left: 900, right: 980, bottom: 140 }, { width: 300, height: 200 }, viewport).left).toBe(692);
    expect(place({ top: 100, left: 600, right: 900, bottom: 140 }, { width: 300, height: 200 }, viewport, { prefer: 'bottom-end' })).toMatchObject({ left: 600, placement: 'bottom-end' });
    expect(place({ top: 100, left: 0, right: 40, bottom: 140 }, { width: 600, height: 200 }, { width: 560, height: 800 }).left).toBe(8);
  });

  it('is a sheet on the bottom edge of a narrow screen', () => {
    expect(place({ top: 400, left: 20, right: 300, bottom: 440 }, { width: 380, height: 500 }, { width: 390, height: 844 })).toEqual({ top: 344, left: 0, maxHeight: 836, placement: 'sheet' });
    expect(place({ top: 400, left: 20, right: 300, bottom: 440 }, { width: 380, height: 900 }, { width: 390, height: 844 }).top).toBe(8);
  });
});

describe('time mask (the row in the popover)', () => {
  it('types, clamps and steps HH:MM:SS', () => {
    expect(typed('140405', 'time')).toEqual({ value: '14:04:05', caret: 8 });
    expect(typed('9', 'time').value).toBe('09:');
    expect(typed('25', 'time').value).toBe('23:');
    expect(step({ value: '23:59:00', caret: 1 }, 1, 'time').value).toBe('00:59:00');
    expect(normalize('9:4', 'time')).toBe('09:04');
  });

  it('reads seconds of the day and formats a time', async () => {
    const { parseTime } = await import('../src/date-mask');

    expect(parseTime('01:02:03')).toBe(3723);
    expect(parseTime('01:02')).toBeNull();
    expect(format(new Date('2026-11-12T14:04:05Z'), 'time')).toBe('14:04:05');
    expect(parse('14:04:05', 'time')).toBeNull();
  });
});

describe('datePickerHTML', () => {
  it('writes a field that works without the script, and escapes', async () => {
    const { datePickerHTML } = await import('../src/date-picker');
    const html = datePickerHTML({ id: 'at', name: 'at', label: 'At "x"', value: '2026-11-12 14:04:05', min: '2026-11-12T00:00:00Z', max: 'now', presets: [{ label: '5m', ago: '5m' }], inputClass: 'input', inputAttrs: { 'data-1p-ignore': true } });

    expect(html).toContain('name="at"');
    expect(html).toContain('value="2026-11-12 14:04:05"');
    expect(html).toContain('pattern="\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}:\\d{2}"');
    expect(html).toContain('class="input datepicker-input"');
    expect(html).toContain('data-1p-ignore');
    expect(html).toContain('aria-label="At &#34;x&#34;"');
    expect(html).toContain('popover="manual"');
    expect(html).toContain('data-max="now"');
  });

  it('writes two fields for a range', async () => {
    const { datePickerHTML } = await import('../src/date-picker');
    const html = datePickerHTML({ id: 'p', name: 'period', label: 'Period', mode: 'range' });

    expect(html).toContain('name="period_from"');
    expect(html).toContain('name="period_to"');
    expect(html).toContain('placeholder="YYYY-MM-DD"');
  });
});
