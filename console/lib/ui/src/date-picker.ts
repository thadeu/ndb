// The date picker: a date, a date and time, or a date range, in UTC.
//
// The field has a positional mask (date-mask.ts). A button opens a calendar
// in a popover (popover.ts) with presets on the left ("5 minutes ago", "Last
// 7 days"); in datetime mode the popover has a time row (HH:MM:SS) with Now
// and Done. Without the script the markup is a text field with a pattern, so
// a form still posts.
//
// datePickerHTML writes the markup (for a server or any framework);
// mountDatePicker brings it to life; createDatePicker does both in the
// browser.

import { addDays, addMonths, clampDate, inRange, isoDay, monthGrid, outside, pickRange, resolvePreset, type Preset } from './calendar-model';
import { erase, format, normalize, parse, parseTime, pattern, step, template, typeDigit, type MaskKind, type MaskState } from './date-mask';
import { closePopover, openPopover, repositionPopover } from './popover';

export type DatePickerMode = 'date' | 'datetime' | 'range';

export interface DatePickerLabels {
  open: string;
  calendar: string;
  time: string;
  now: string;
  done: string;
  previous: string;
  next: string;
  hintDate: string;
  hintDatetime: string;
  hintRange: string;
}

export const DEFAULT_LABELS: DatePickerLabels = {
  open: 'open the calendar',
  calendar: 'calendar',
  time: 'Time (UTC)',
  now: 'Now',
  done: 'Done',
  previous: 'Previous month',
  next: 'Next month',
  hintDate: 'Dates in UTC.',
  hintDatetime: 'Pick a day and a time (UTC).',
  hintRange: 'Pick the first and the last day (UTC).',
};

// DatePickerChange is what onChange and the datepicker:change event get.
// value and values are the fields as typed; date, from and to are parsed
// (null while a field is not complete).
export interface DatePickerChange {
  value: string;
  values: string[];
  date: Date | null;
  from: Date | null;
  to: Date | null;
}

export interface DatePickerCallbacks {
  onChange?: (change: DatePickerChange) => void;
}

// DatePickerBounds can change after mount (update).
export interface DatePickerBounds {
  min?: string | null;
  max?: string | null;
  presets?: Preset[];
}

export interface DatePickerOptions extends DatePickerCallbacks {
  id: string;
  name: string;
  label: string;
  mode?: DatePickerMode;
  value?: string;
  from?: string;
  to?: string;
  // min and max are RFC 3339 times; max "now" follows the clock.
  min?: string;
  max?: string;
  presets?: Preset[];
  required?: boolean;
  // inputClass is added to the class of each field (your input style).
  inputClass?: string;
  // inputAttrs are more attributes for each field (true writes a bare one).
  inputAttrs?: Record<string, string | boolean>;
  locale?: string;
  labels?: Partial<DatePickerLabels>;
}

export interface DatePickerController {
  readonly root: HTMLElement;
  // value is the first field; values are all of them (two for a range).
  value(): string;
  values(): string[];
  // date is the first field parsed; null while it is not complete.
  date(): Date | null;
  // setValue writes a field (0, or 1 for the end of a range) from a Date or
  // a text in the shape of the mask; null clears it. It fires a change.
  setValue(value: Date | string | null, index?: number): void;
  // update changes min, max or the presets.
  update(bounds: DatePickerBounds): void;
  open(): void;
  close(): void;
  // destroy removes every listener; the markup stays.
  destroy(): void;
}

function esc(text: string): string {
  return text.replace(/[&<>"']/g, ch => `&#${ch.charCodeAt(0)};`);
}

function attrs(list: Record<string, string | boolean | undefined>): string {
  return Object.entries(list)
    .filter(([, value]) => value !== undefined && value !== false)
    .map(([key, value]) => (value === true ? key : `${key}="${esc(String(value))}"`))
    .join(' ');
}

function kindOf(mode: DatePickerMode): MaskKind {
  return mode === 'datetime' ? 'datetime' : 'date';
}

const ICON =
  '<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><rect x="2" y="3" width="12" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.4"/><path d="M2 6.5h12M5.5 1.5v3M10.5 1.5v3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>';

// datePickerHTML is the markup of a date picker. A range posts {name}_from
// and {name}_to.
export function datePickerHTML(options: DatePickerOptions): string {
  const { id, name, label, mode = 'datetime', presets = [], required = false, inputClass = '', inputAttrs = {} } = options;
  const kind = kindOf(mode);
  const labels = { ...DEFAULT_LABELS, ...options.labels };

  const field = (fieldId: string, fieldName: string, value: string | undefined, aria: string) =>
    `<input ${attrs({
      ...inputAttrs,
      class: `${inputClass} datepicker-input`.trim(),
      id: fieldId,
      name: fieldName,
      value: value ?? '',
      placeholder: template(kind),
      pattern: pattern(kind),
      inputmode: 'numeric',
      autocomplete: 'off',
      spellcheck: 'false',
      maxlength: String(template(kind).length),
      'aria-label': aria,
      required,
      'data-datepicker-input': true,
    })} />`;

  const fields = mode === 'range' ? `${field(id, `${name}_from`, options.from, `${label}: from`)}<span class="datepicker-sep" aria-hidden="true">→</span>${field(`${id}-to`, `${name}_to`, options.to, `${label}: to`)}` : field(id, name, options.value, label);

  const root = attrs({
    class: 'datepicker',
    'data-datepicker': true,
    'data-id': id,
    'data-mode': mode,
    'data-min': options.min,
    'data-max': options.max,
    'data-locale': options.locale,
    'data-presets': JSON.stringify(presets),
    'data-labels': options.labels ? JSON.stringify(options.labels) : undefined,
  });

  const toggle = attrs({ type: 'button', class: 'datepicker-toggle', 'aria-label': `${label}: ${labels.open}`, 'aria-haspopup': 'dialog', 'aria-expanded': 'false', 'aria-controls': `${id}-calendar` });
  const pop = attrs({ id: `${id}-calendar`, class: 'popover-layer datepicker-pop', popover: 'manual', role: 'dialog', 'aria-label': `${label}: ${labels.calendar}` });

  return `<div ${root}><div class="datepicker-field">${fields}<button ${toggle}>${ICON}</button></div><div ${pop}></div></div>`;
}

// attachMask makes an input follow the mask, on the keyboard and on mobile
// (beforeinput carries what a soft keyboard types).
export function attachMask(input: HTMLInputElement, kind: MaskKind, onChange: () => void = () => {}, signal?: AbortSignal): void {
  const apply = (next: MaskState) => {
    input.value = next.value;
    input.setSelectionRange(next.caret, next.caret);
    onChange();
  };

  const state = (): MaskState => {
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? start;

    if (end - start === input.value.length && input.value.length > 0) return { value: '', caret: 0 };

    return { value: input.value, caret: start };
  };

  input.addEventListener('beforeinput', event => {
    const type = event.inputType;

    if (type === 'insertText' && event.data) {
      event.preventDefault();

      let next = state();

      for (const ch of event.data) next = typeDigit(next, ch, kind);

      apply(next);
    } else if (type === 'deleteContentBackward' || type === 'deleteContentForward') {
      event.preventDefault();

      const current = state();

      apply(current.value === '' ? current : erase(current, kind, type === 'deleteContentForward'));
    } else if (type.startsWith('insertFrom') || type === 'insertReplacementText') {
      event.preventDefault();

      const value = normalize(event.data ?? event.dataTransfer?.getData('text/plain') ?? '', kind);

      apply({ value, caret: value.length });
    } else if (type.startsWith('delete')) {
      event.preventDefault();
      apply({ value: '', caret: 0 });
    }
  }, { signal });

  input.addEventListener('keydown', event => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;

    event.preventDefault();
    apply(step(state(), event.key === 'ArrowUp' ? 1 : -1, kind));
  }, { signal });
}

type Attrs = Record<string, string | boolean | undefined>;

function h(tag: string, list: Attrs = {}, ...children: (Node | string)[]): HTMLElement {
  const el = document.createElement(tag);

  for (const [key, value] of Object.entries(list)) {
    if (value === true) el.setAttribute(key, '');
    else if (typeof value === 'string') el.setAttribute(key, value);
  }

  el.append(...children);

  return el;
}

function button(label: string, className: string, onClick: () => void, extra: Attrs = {}): HTMLElement {
  const el = h('button', { type: 'button', class: className, ...extra }, label);

  el.addEventListener('click', onClick);

  return el;
}

class Picker implements DatePickerController {
  private readonly id: string;
  private readonly mode: DatePickerMode;
  private readonly kind: MaskKind;
  private readonly inputs: HTMLInputElement[];
  private readonly pop: HTMLElement;
  private readonly toggle: HTMLElement;
  private readonly labels: DatePickerLabels;
  private readonly locale: string | undefined;
  private readonly abort = new AbortController();
  private presets: Preset[];
  private view = { year: 0, month: 0 };
  private focusDay = '';
  private time = '';

  constructor(readonly root: HTMLElement, private readonly callbacks: DatePickerCallbacks = {}) {
    const data = root.dataset;

    this.id = data.id ?? '';
    this.mode = (data.mode as DatePickerMode) ?? 'datetime';
    this.kind = kindOf(this.mode);
    this.inputs = [...root.querySelectorAll<HTMLInputElement>('[data-datepicker-input]')];
    this.pop = root.querySelector<HTMLElement>('.datepicker-pop') as HTMLElement;
    this.toggle = root.querySelector<HTMLElement>('.datepicker-toggle') as HTMLElement;
    this.presets = JSON.parse(data.presets || '[]') as Preset[];
    this.labels = { ...DEFAULT_LABELS, ...(JSON.parse(data.labels || '{}') as Partial<DatePickerLabels>) };
    this.locale = data.locale;

    const signal = this.abort.signal;

    this.toggle.addEventListener('click', event => {
      event.preventDefault();

      if (this.isOpen()) this.close();
      else this.open();
    }, { signal });

    for (const input of this.inputs) attachMask(input, this.kind, () => this.fieldChanged(), signal);
  }

  value(): string {
    return this.inputs[0]?.value ?? '';
  }

  values(): string[] {
    return this.inputs.map(input => input.value);
  }

  date(): Date | null {
    return this.dates()[0] ?? null;
  }

  setValue(value: Date | string | null, index = 0): void {
    const text = value instanceof Date ? format(value, this.kind) : normalize(value ?? '', this.kind);
    const input = this.inputs[index];

    if (!input || input.value === text) return;

    input.value = text;
    this.changed();

    if (this.isOpen()) this.render();
  }

  update(bounds: DatePickerBounds): void {
    const data = this.root.dataset;

    if (bounds.min !== undefined) bounds.min === null ? delete data.min : (data.min = bounds.min);
    if (bounds.max !== undefined) bounds.max === null ? delete data.max : (data.max = bounds.max);

    if (bounds.presets) {
      this.presets = bounds.presets;
      data.presets = JSON.stringify(bounds.presets);
    }

    if (this.isOpen()) this.render();
  }

  destroy(): void {
    if (this.isOpen()) this.close();

    this.abort.abort();
    mounted.delete(this.root);
  }

  // changed tells the page: the callback and a bubbling datepicker:change.
  private changed(): void {
    const dates = this.dates();
    const detail: DatePickerChange = { value: this.value(), values: this.values(), date: dates[0] ?? null, from: this.mode === 'range' ? (dates[0] ?? null) : null, to: dates[1] ?? null };

    this.callbacks.onChange?.(detail);
    this.root.dispatchEvent(new CustomEvent<DatePickerChange>('datepicker:change', { detail, bubbles: true }));
  }

  open(): void {
    const value = this.dates()[0];
    const first = value ?? clampDate(new Date(), this.min(), this.max());

    this.view = { year: first.getUTCFullYear(), month: first.getUTCMonth() };
    this.focusDay = isoDay(first);
    this.time = format(first, 'time');
    this.render();
    openPopover(this.pop, this.toggle, { prefer: 'bottom-end' });
    this.pop.querySelector<HTMLElement>('.dp-day[tabindex="0"]')?.focus({ preventScroll: true });
  }

  close(): void {
    closePopover();
  }

  private isOpen(): boolean {
    return this.pop.classList.contains('open');
  }

  private min(): Date | undefined {
    return this.bound(this.root.dataset.min);
  }

  private max(): Date | undefined {
    return this.bound(this.root.dataset.max);
  }

  private bound(text?: string): Date | undefined {
    if (!text) return undefined;
    if (text === 'now') return new Date();

    const date = new Date(text);

    return Number.isNaN(date.getTime()) ? undefined : date;
  }

  private dates(): (Date | null)[] {
    return this.inputs.map(input => parse(input.value, this.kind));
  }

  private set(index: number, date: Date | undefined): void {
    const input = this.inputs[index];

    if (!input) return;

    input.value = date ? format(date, this.kind) : '';
    input.dispatchEvent(new Event('change', { bubbles: true }));
    this.changed();
  }

  // setAt writes day + time (clamped) in the field, and keeps the time row
  // in step with what was written.
  private setAt(day: string, time: string): void {
    const date = clampDate(new Date(`${day}T${time}Z`), this.min(), this.max());

    this.set(0, date);
    this.time = format(date, 'time');
    this.focusDay = isoDay(date);
  }

  private fieldChanged(): void {
    this.changed();

    const date = this.dates()[0];

    if (date && this.kind === 'datetime') this.time = format(date, 'time');

    if (this.isOpen()) this.render();
  }

  // choose is a click on a day. A date closes; a date and time stays open for
  // the time row; a range takes the first and the second click.
  private choose(iso: string): void {
    if (this.mode === 'range') {
      const current = this.dates().map(date => (date ? isoDay(date) : undefined));
      const next = pickRange({ from: current[0], to: current[1] }, iso);

      this.set(0, new Date(`${next.from}T00:00:00Z`));
      this.set(1, next.to ? new Date(`${next.to}T00:00:00Z`) : undefined);
      this.focusDay = iso;
      this.render();

      if (next.to) this.done();

      return;
    }

    if (this.kind === 'date') {
      this.set(0, clampDate(new Date(`${iso}T00:00:00Z`), this.min(), this.max()));
      this.done();

      return;
    }

    this.setAt(iso, parseTime(this.time) === null ? '00:00:00' : this.time);
    this.render();
    this.timeInput()?.focus({ preventScroll: true });
  }

  private timeInput(): HTMLInputElement | null {
    return this.pop.querySelector<HTMLInputElement>('.dp-time-input');
  }

  private applyTime(): void {
    if (parseTime(this.time) === null) return;

    const date = this.dates()[0];

    this.setAt(date ? isoDay(date) : this.focusDay, this.time);
  }

  private now(): void {
    const date = clampDate(new Date(), this.min(), this.max());

    this.set(0, date);
    this.done();
  }

  private done(): void {
    this.close();
    this.inputs[this.mode === 'range' ? 1 : 0]?.focus();
  }

  private preset(preset: Preset): void {
    const picked = resolvePreset(preset, new Date(), this.min(), this.max());

    if (this.mode === 'range') {
      this.set(0, picked.from);
      this.set(1, picked.to);
    } else {
      this.set(0, picked.at ?? picked.from);
    }

    this.done();
  }

  private move(delta: number): void {
    this.view = addMonths(this.view.year, this.view.month, delta);
    this.render();
  }

  private monthTitle(): string {
    return new Intl.DateTimeFormat(this.locale, { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(this.view.year, this.view.month, 1)));
  }

  private weekdays(): string[] {
    const fmt = new Intl.DateTimeFormat(this.locale, { weekday: 'short', timeZone: 'UTC' });

    return Array.from({ length: 7 }, (_, i) => fmt.format(new Date(Date.UTC(2024, 0, 1 + i))));
  }

  private grid(): HTMLElement {
    const [min, max] = [this.min(), this.max()];
    const values = this.dates().map(date => (date ? isoDay(date) : undefined));
    const days = monthGrid(this.view.year, this.view.month, isoDay(new Date()));

    if (!days.some(day => day.iso === this.focusDay && day.inMonth)) this.focusDay = days.find(day => day.inMonth && !outside(day.iso, min, max))?.iso ?? days[7]?.iso ?? '';

    const grid = h('div', { class: 'dp-grid', role: 'grid', 'aria-label': this.monthTitle() }, ...this.weekdays().map(name => h('span', { class: 'dp-weekday', 'aria-hidden': 'true' }, name)));

    for (const day of days) {
      const on = day.iso === values[0] || day.iso === values[1];
      const classes = ['dp-day', day.inMonth ? '' : 'out', day.today ? 'today' : '', on ? 'on' : '', inRange(day.iso, values[0], values[1]) ? 'in-range' : ''].filter(Boolean).join(' ');

      grid.append(button(String(day.day), classes, () => this.choose(day.iso), { 'data-day': day.iso, tabindex: day.iso === this.focusDay ? '0' : '-1', disabled: outside(day.iso, min, max), 'aria-pressed': on ? 'true' : 'false', 'aria-label': day.iso }));
    }

    grid.addEventListener('keydown', event => this.gridKey(event));

    return grid;
  }

  private timeRow(): HTMLElement {
    const inputId = `${this.id}-time`;
    const input = h('input', { id: inputId, class: 'dp-time-input', value: this.time, placeholder: template('time'), inputmode: 'numeric', autocomplete: 'off', spellcheck: 'false', maxlength: '8', 'aria-label': this.labels.time }) as HTMLInputElement;

    input.value = this.time;

    attachMask(input, 'time', () => {
      this.time = input.value;

      if (parseTime(this.time) !== null) {
        this.applyTime();
        this.renderKeepingTimeCaret(input.selectionStart ?? this.time.length);
      }
    });

    input.addEventListener('keydown', event => {
      if (event.key !== 'Enter') return;

      event.preventDefault();
      this.applyTime();
      this.done();
    });

    return h('div', { class: 'dp-time' }, h('label', { class: 'dp-time-label', for: inputId }, this.labels.time), input, button(this.labels.now, 'dp-btn', () => this.now()), button(this.labels.done, 'dp-btn primary', () => (this.applyTime(), this.done())));
  }

  private renderKeepingTimeCaret(caret: number): void {
    this.render();

    const input = this.timeInput();

    input?.focus({ preventScroll: true });
    input?.setSelectionRange(caret, caret);
  }

  private render(): void {
    const prev = button('‹', 'dp-nav', () => this.move(-1), { 'aria-label': this.labels.previous });
    const next = button('›', 'dp-nav', () => this.move(1), { 'aria-label': this.labels.next });
    const head = h('div', { class: 'dp-head' }, prev, h('span', { class: 'dp-title' }, this.monthTitle()), next);
    const hint = this.mode === 'range' ? this.labels.hintRange : this.kind === 'datetime' ? this.labels.hintDatetime : this.labels.hintDate;
    const main = h('div', { class: 'dp-main' }, head, this.grid(), ...(this.kind === 'datetime' ? [this.timeRow()] : []), h('p', { class: 'dp-hint' }, hint));
    const side = this.presets.map(preset => button(preset.label, 'dp-preset', () => this.preset(preset)));
    const panel = h('div', { class: side.length > 0 ? 'dp-panel with-presets' : 'dp-panel' }, ...(side.length > 0 ? [h('div', { class: 'dp-presets' }, ...side)] : []), main);

    this.pop.replaceChildren(panel);
    repositionPopover();
  }

  private gridKey(event: KeyboardEvent): void {
    const moves: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
    const delta = moves[event.key];

    if (delta === undefined) return;

    event.preventDefault();

    const iso = addDays(this.focusDay, delta);
    const date = new Date(`${iso}T00:00:00Z`);

    this.focusDay = iso;
    this.view = { year: date.getUTCFullYear(), month: date.getUTCMonth() };
    this.render();
    this.pop.querySelector<HTMLElement>(`[data-day="${iso}"]`)?.focus({ preventScroll: true });
  }
}

const mounted = new WeakMap<HTMLElement, Picker>();

// mountDatePicker brings one datePickerHTML root to life (once; a second
// call returns the same picker).
export function mountDatePicker(root: HTMLElement, callbacks: DatePickerCallbacks = {}): DatePickerController {
  const existing = mounted.get(root);

  if (existing) return existing;

  const picker = new Picker(root, callbacks);

  mounted.set(root, picker);

  return picker;
}

// mountDatePickers mounts every date picker under root.
export function mountDatePickers(root: ParentNode = document, callbacks: DatePickerCallbacks = {}): DatePickerController[] {
  return [...root.querySelectorAll<HTMLElement>('[data-datepicker]')].map(el => mountDatePicker(el, callbacks));
}

// createDatePicker writes a date picker into container and mounts it.
export function createDatePicker(container: HTMLElement, options: DatePickerOptions): DatePickerController {
  container.innerHTML = datePickerHTML(options);

  return mountDatePicker(container.firstElementChild as HTMLElement, { onChange: options.onChange });
}
