// enhance turns the inputs that a page already has into a date picker: the
// form builder of Rails (or any server) writes the input, with its name,
// value and validations, and the picker grows around it. restore undoes it,
// so a Stimulus disconnect leaves the markup as it was (and a Turbo cache
// snapshot clean).
//
//   <div data-tdu-datepicker data-tdu-datepicker-max="now">
//     <input type="text" name="restore[at]" value="2026-11-08 14:04:05">
//   </div>

import { datePickerHTML, mountDatePicker, type DatePickerCallbacks, type DatePickerController, type DatePickerLabels, type DatePickerMode } from './date-picker';
import { normalize } from './date-mask';
import type { Preset } from './calendar-model';

export interface EnhanceOptions extends DatePickerCallbacks {
  // mode defaults to range with two inputs, date for an input of type
  // date, and datetime otherwise.
  mode?: DatePickerMode;
  min?: string;
  max?: string;
  presets?: Preset[];
  locale?: string;
  labels?: Partial<DatePickerLabels>;
  // label defaults to the text of the input's <label>, then its name.
  label?: string;
}

export interface EnhancedDatePicker extends DatePickerController {
  // restore removes the picker and gives the inputs back as they were.
  restore(): void;
}

const MASK_ATTRS = ['pattern', 'placeholder', 'inputmode', 'autocomplete', 'spellcheck', 'maxlength', 'aria-label', 'data-datepicker-input'];

const ROOT_DATA = ['data-datepicker', 'data-id', 'data-mode', 'data-min', 'data-max', 'data-locale', 'data-presets', 'data-labels'];

let seq = 0;

function inputsOf(root: HTMLElement): HTMLInputElement[] {
  return [...root.querySelectorAll<HTMLInputElement>('input:not([type="hidden"]):not([type="checkbox"]):not([type="radio"]):not([type="submit"])')].slice(0, 2);
}

function modeOf(inputs: HTMLInputElement[]): DatePickerMode {
  if (inputs.length > 1) return 'range';

  return inputs[0]?.type === 'date' ? 'date' : 'datetime';
}

function labelOf(input: HTMLInputElement): string {
  return input.labels?.[0]?.textContent?.trim() || input.getAttribute('aria-label') || input.name || 'Date';
}

// clean removes what a cached copy of an enhanced root still has (a Turbo
// snapshot keeps the markup but not the listeners).
function clean(root: HTMLElement): void {
  const field = root.querySelector<HTMLElement>(':scope > .datepicker-field');

  if (!field) return;

  field.replaceWith(...field.querySelectorAll('input'));
  root.querySelector(':scope > .datepicker-pop')?.remove();
}

export function enhance(root: HTMLElement, options: EnhanceOptions = {}): EnhancedDatePicker {
  clean(root);

  const inputs = inputsOf(root);
  const first = inputs[0];

  if (!first) throw new Error('@tdu/ui enhance: no input inside the element');

  const mode = options.mode ?? modeOf(inputs);
  const label = options.label ?? labelOf(first);
  const generatedId = !first.id;

  if (generatedId) first.id = `tdu-dp-${++seq}`;

  const template = document.createElement('div');

  template.innerHTML = datePickerHTML({ id: first.id, name: 'x', label, mode, min: options.min, max: options.max, presets: options.presets, locale: options.locale, labels: options.labels });

  const made = template.firstElementChild as HTMLElement;
  const field = made.querySelector('.datepicker-field') as HTMLElement;
  const pop = made.querySelector('.datepicker-pop') as HTMLElement;
  const slots = [...field.querySelectorAll<HTMLInputElement>('input')];

  // The field goes where the first input is.
  const marker = document.createComment('tdu-datepicker');

  first.before(marker);

  // Each real input takes the place of a made one, with its mask attributes
  // (its own placeholder and aria-label stay).
  const saved = inputs.map((input, i) => ({ input, slot: slots[i], before: { type: input.getAttribute('type'), className: input.className, attrs: new Map(MASK_ATTRS.map(name => [name, input.getAttribute(name)])) } }));

  for (const { input, slot } of saved) {
    if (!slot) continue;

    const native = input.type;

    if (native === 'date' || native === 'datetime-local') {
      const local = native === 'datetime-local' && /T\d\d:\d\d$/.test(input.value) ? `${input.value}:00` : input.value;

      input.type = 'text';
      input.value = normalize(local, mode === 'datetime' ? 'datetime' : 'date');
    }

    for (const name of MASK_ATTRS) {
      const value = slot.getAttribute(name);
      const own = (name === 'placeholder' || name === 'aria-label') && input.hasAttribute(name);

      if (value !== null && !own) input.setAttribute(name, value);
    }

    input.classList.add('datepicker-input');
    slot.replaceWith(input);
  }

  marker.replaceWith(field);
  root.append(pop);

  const rootBefore = { className: root.className, attrs: new Map(ROOT_DATA.map(name => [name, root.getAttribute(name)])) };

  root.classList.add('datepicker');

  for (const name of ROOT_DATA) {
    const value = made.getAttribute(name);

    if (value !== null) root.setAttribute(name, value);
  }

  const picker = mountDatePicker(root, { onChange: options.onChange });

  const restore = () => {
    picker.destroy();
    field.replaceWith(...inputs);
    pop.remove();

    for (const { input, before } of saved) {
      if (before.type !== null) input.setAttribute('type', before.type);

      input.className = before.className;

      for (const [name, value] of before.attrs) {
        if (value === null) input.removeAttribute(name);
        else input.setAttribute(name, value);
      }
    }

    if (generatedId) first.removeAttribute('id');

    root.className = rootBefore.className;

    for (const [name, value] of rootBefore.attrs) {
      if (value === null) root.removeAttribute(name);
      else root.setAttribute(name, value);
    }
  };

  return {
    root,
    value: () => picker.value(),
    values: () => picker.values(),
    date: () => picker.date(),
    setValue: (value, index) => picker.setValue(value, index),
    update: bounds => picker.update(bounds),
    open: () => picker.open(),
    close: () => picker.close(),
    destroy: () => picker.destroy(),
    restore,
  };
}

// optionsOf reads the data-tdu-datepicker-* attributes of an element.
export function optionsOf(el: HTMLElement): EnhanceOptions {
  const data = el.dataset;
  const json = <T>(text: string | undefined): T | undefined => (text ? (JSON.parse(text) as T) : undefined);

  return {
    mode: (data.tduDatepicker || undefined) as DatePickerMode | undefined,
    min: data.tduDatepickerMin,
    max: data.tduDatepickerMax,
    locale: data.tduDatepickerLocale,
    label: data.tduDatepickerLabel,
    presets: json<Preset[]>(data.tduDatepickerPresets),
    labels: json<Partial<DatePickerLabels>>(data.tduDatepickerLabels),
  };
}

// observeDatePickers enhances every [data-tdu-datepicker] under root, now and
// whenever the DOM changes (Turbo Drive, Frames and Streams), and restores
// the ones that leave the page. It returns a stop function. Without
// Stimulus, this is all a Hotwire app needs.
export function observeDatePickers(root: ParentNode & Node = document, callbacks: DatePickerCallbacks = {}): () => void {
  const live = new Map<HTMLElement, EnhancedDatePicker>();

  const scan = () => {
    for (const [el, picker] of live) {
      if (!el.isConnected) {
        picker.destroy();
        live.delete(el);
      }
    }

    for (const el of root.querySelectorAll<HTMLElement>('[data-tdu-datepicker]')) {
      if (!live.has(el)) live.set(el, enhance(el, { ...optionsOf(el), ...callbacks }));
    }
  };

  scan();

  const observer = new MutationObserver(scan);

  observer.observe(root, { childList: true, subtree: true });

  return () => {
    observer.disconnect();

    for (const picker of live.values()) picker.restore();

    live.clear();
  };
}
