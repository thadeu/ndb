import type { FC } from 'hono/jsx';

import { addDays, addMonths, clampDate, inRange, isoDay, monthGrid, MONTHS, outside, pickRange, resolvePreset, WEEKDAYS, type Preset } from './calendar-model';
import { erase, format, normalize, parse, pattern, step, template, typeDigit, type MaskKind, type MaskState } from './date-mask';
import { Input } from './input';
import { closePopover, openPopover, Popover, popoverTrigger } from './popover';

export type DatePickerMode = 'date' | 'datetime' | 'range';

export interface DatePickerProps {
  id: string;
  name: string;
  label: string;
  mode?: DatePickerMode;
  value?: string;
  from?: string;
  to?: string;
  min?: string;
  max?: string;
  presets?: Preset[];
  required?: boolean;
}

// DatePicker is a date, a date and time, or a date range, in UTC. Without
// the script it is a text field with a pattern. With it, the field has a
// positional mask (date-mask.ts) and a button opens a calendar in a popover,
// with presets on the left ("5 minutes ago", "Last 7 days"). A range posts
// {name}_from and {name}_to. max "now" follows the clock.
export const DatePicker: FC<DatePickerProps> = ({ id, name, label, mode = 'datetime', value, from, to, min, max, presets = [], required = false }) => {
  const kind: MaskKind = mode === 'datetime' ? 'datetime' : 'date';
  const popId = `${id}-calendar`;

  const field = (inputId: string, inputName: string, inputValue: string | undefined, aria: string) => (
    <Input
      id={inputId}
      mono
      name={inputName}
      value={inputValue ?? ''}
      placeholder={template(kind)}
      pattern={pattern(kind)}
      inputmode="numeric"
      autocomplete="off"
      maxlength={template(kind).length}
      aria-label={aria}
      required={required}
      data-datepicker-input
    />
  );

  return (
    <div class="datepicker" data-datepicker data-mode={mode} data-min={min} data-max={max} data-presets={JSON.stringify(presets)}>
      <div class="datepicker-field">
        {mode === 'range' ? (
          <>
            {field(id, `${name}_from`, from, `${label}: from`)}

            <span class="datepicker-sep" aria-hidden="true">
              →
            </span>

            {field(`${id}-to`, `${name}_to`, to, `${label}: to`)}
          </>
        ) : (
          field(id, name, value, label)
        )}

        <button type="button" class="datepicker-toggle" aria-label={`${label}: open the calendar`} {...popoverTrigger(popId)}>
          <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
            <rect x="2" y="3" width="12" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.4" />
            <path d="M2 6.5h12M5.5 1.5v3M10.5 1.5v3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
        </button>
      </div>

      <Popover id={popId} label={`${label}: calendar`} class="datepicker-pop" />
    </div>
  );
};

// The browser side. The calendar is built with the DOM; the state is the
// month on view and the values of the fields.

type Attrs = Record<string, string | boolean | undefined>;

function h(tag: string, attrs: Attrs = {}, ...children: (Node | string)[]): HTMLElement {
  const el = document.createElement(tag);

  for (const [key, value] of Object.entries(attrs)) {
    if (value === true) el.setAttribute(key, '');
    else if (typeof value === 'string') el.setAttribute(key, value);
  }

  el.append(...children);

  return el;
}

// attachMask makes an input follow the mask, on the keyboard and on mobile
// (beforeinput carries what a soft keyboard types).
export function attachMask(input: HTMLInputElement, kind: MaskKind, onChange: () => void): void {
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

      const text = event.data ?? event.dataTransfer?.getData('text/plain') ?? '';
      const value = normalize(text, kind);

      apply({ value, caret: value.length });
    } else if (type === 'deleteByCut' || type.startsWith('delete')) {
      event.preventDefault();
      apply({ value: '', caret: 0 });
    }
  });

  input.addEventListener('keydown', event => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;

    event.preventDefault();
    apply(step(state(), event.key === 'ArrowUp' ? 1 : -1, kind));
  });
}

class Picker {
  private readonly mode: DatePickerMode;
  private readonly kind: MaskKind;
  private readonly inputs: HTMLInputElement[];
  private readonly pop: HTMLElement;
  private readonly toggle: HTMLElement;
  private readonly presets: Preset[];
  private view = { year: 0, month: 0 };
  private focusDay = '';

  constructor(private readonly root: HTMLElement) {
    this.mode = (root.dataset.mode as DatePickerMode) ?? 'datetime';
    this.kind = this.mode === 'datetime' ? 'datetime' : 'date';
    this.inputs = [...root.querySelectorAll<HTMLInputElement>('[data-datepicker-input]')];
    this.pop = root.querySelector<HTMLElement>('.popover-layer') as HTMLElement;
    this.toggle = root.querySelector<HTMLElement>('.datepicker-toggle') as HTMLElement;
    this.presets = JSON.parse(root.dataset.presets || '[]') as Preset[];

    this.toggle.removeAttribute('data-popover-open');
    this.toggle.addEventListener('click', event => {
      event.preventDefault();

      if (this.pop.classList.contains('open')) closePopover();
      else this.open();
    });

    for (const input of this.inputs) attachMask(input, this.kind, () => this.pop.classList.contains('open') && this.render());
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

  private values(): (Date | null)[] {
    return this.inputs.map(input => parse(input.value, this.kind));
  }

  private open(): void {
    const first = this.values()[0] ?? clampDate(new Date(), this.min(), this.max());

    this.view = { year: first.getUTCFullYear(), month: first.getUTCMonth() };
    this.focusDay = isoDay(first);
    this.render();
    openPopover(this.pop, this.toggle, { prefer: 'bottom-end' });
    this.pop.querySelector<HTMLElement>('.dp-day[tabindex="0"]')?.focus({ preventScroll: true });
  }

  private set(index: number, date: Date | undefined): void {
    const input = this.inputs[index];

    if (input) input.value = date ? format(date, this.kind) : '';
  }

  // choose is a click on a day: a single value keeps its time of day (or the
  // time now for a new one); a range takes the first and the second click.
  private choose(iso: string): void {
    const [min, max] = [this.min(), this.max()];

    if (this.mode === 'range') {
      const current = this.values().map(date => (date ? isoDay(date) : undefined));
      const next = pickRange({ from: current[0], to: current[1] }, iso);

      this.set(0, new Date(`${next.from}T00:00:00Z`));
      this.set(1, next.to ? new Date(`${next.to}T00:00:00Z`) : undefined);
      this.focusDay = iso;
      this.render();

      if (next.to) this.done();

      return;
    }

    const old = this.values()[0] ?? new Date();
    const time = old.toISOString().slice(11, 19);

    this.set(0, clampDate(new Date(`${iso}T${this.kind === 'datetime' ? time : '00:00:00'}Z`), min, max));
    this.done();
  }

  private done(): void {
    closePopover();
    this.inputs[this.mode === 'range' ? 1 : 0]?.focus();
  }

  private preset(preset: Preset): void {
    const picked = resolvePreset(preset, new Date(), this.min(), this.max());

    if (this.mode === 'range') {
      this.set(0, picked.from);
      this.set(1, picked.to);
    } else {
      this.set(0, picked.at);
    }

    this.done();
  }

  private move(delta: number): void {
    this.view = addMonths(this.view.year, this.view.month, delta);
    this.render();
  }

  private render(): void {
    const [min, max] = [this.min(), this.max()];
    const values = this.values().map(date => (date ? isoDay(date) : undefined));
    const today = isoDay(new Date());
    const days = monthGrid(this.view.year, this.view.month, today);

    if (!days.some(day => day.iso === this.focusDay && day.inMonth)) this.focusDay = days.find(day => day.inMonth && !outside(day.iso, min, max))?.iso ?? days[7]?.iso ?? '';

    const grid = h('div', { class: 'dp-grid', role: 'grid', 'aria-label': `${MONTHS[this.view.month]} ${this.view.year}` }, ...WEEKDAYS.map(name => h('span', { class: 'dp-weekday', 'aria-hidden': 'true' }, name)));

    for (const day of days) {
      const on = day.iso === values[0] || day.iso === values[1];
      const classes = ['dp-day', day.inMonth ? '' : 'out', day.today ? 'today' : '', on ? 'on' : '', inRange(day.iso, values[0], values[1]) ? 'in-range' : ''].filter(Boolean).join(' ');

      const button = h('button', { type: 'button', class: classes, 'data-day': day.iso, tabindex: day.iso === this.focusDay ? '0' : '-1', disabled: outside(day.iso, min, max), 'aria-pressed': on ? 'true' : 'false', 'aria-label': day.iso }, String(day.day));

      button.addEventListener('click', () => this.choose(day.iso));
      grid.append(button);
    }

    grid.addEventListener('keydown', event => this.gridKey(event));

    const prev = h('button', { type: 'button', class: 'dp-nav', 'aria-label': 'Previous month' }, '‹');
    const next = h('button', { type: 'button', class: 'dp-nav', 'aria-label': 'Next month' }, '›');

    prev.addEventListener('click', () => this.move(-1));
    next.addEventListener('click', () => this.move(1));

    const head = h('div', { class: 'dp-head' }, prev, h('span', { class: 'dp-title' }, `${MONTHS[this.view.month]} ${this.view.year}`), next);
    const hint = this.kind === 'datetime' ? 'Pick a day; type the time in the field (UTC).' : this.mode === 'range' ? 'Pick the first and the last day (UTC).' : 'Dates in UTC.';
    const main = h('div', { class: 'dp-main' }, head, grid, h('p', { class: 'dp-hint' }, hint));

    const side = this.presets.map(preset => {
      const button = h('button', { type: 'button', class: 'dp-preset' }, preset.label);

      button.addEventListener('click', () => this.preset(preset));

      return button;
    });

    this.pop.replaceChildren(h('div', { class: side.length > 0 ? 'dp-panel with-presets' : 'dp-panel' }, ...(side.length > 0 ? [h('div', { class: 'dp-presets' }, ...side)] : []), main));
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

export function mount(): void {
  for (const root of document.querySelectorAll<HTMLElement>('[data-datepicker]')) {
    if (root.dataset.mounted) continue;

    root.dataset.mounted = '1';
    new Picker(root);
  }
}

