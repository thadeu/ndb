// React bindings: useDatePicker and <DatePicker>. The picker owns the DOM
// of its container (React renders an empty div and never touches its
// children), so it works the same in React, on a server page or anywhere.
//
//   const at = useDatePicker({ name: 'at', label: 'At (UTC)', max: 'now' });
//   return <div ref={at.ref} />;     // at.value, at.date, at.setValue(...)
//
//   <DatePicker name="at" label="At (UTC)" value={at} onChange={c => setAt(c.value)} />

import { createElement, useCallback, useEffect, useId, useRef, useState, type ReactElement } from 'react';

import { createDatePicker, type DatePickerChange, type DatePickerController, type DatePickerOptions } from './date-picker';

export interface UseDatePickerOptions extends Omit<DatePickerOptions, 'id'> {
  // id defaults to one from useId.
  id?: string;
}

export interface UseDatePicker {
  // ref goes on the container element (a div).
  ref: (el: HTMLElement | null) => void;
  value: string;
  values: string[];
  date: Date | null;
  from: Date | null;
  to: Date | null;
  setValue: (value: Date | string | null, index?: number) => void;
  open: () => void;
  close: () => void;
  // picker is the controller of the vanilla API, once mounted.
  picker: DatePickerController | null;
}

const EMPTY: DatePickerChange = { value: '', values: [], date: null, from: null, to: null };

export function useDatePicker(options: UseDatePickerOptions): UseDatePicker {
  const auto = useId();
  const id = options.id ?? `dp${auto.replace(/[^a-zA-Z0-9]/g, '')}`;

  const [change, setChange] = useState<DatePickerChange>({ ...EMPTY, value: options.value ?? '', values: [options.value ?? ''] });
  const [picker, setPicker] = useState<DatePickerController | null>(null);

  const latest = useRef(options);

  latest.current = options;

  // The picker is made again only when its shape changes; min, max, presets
  // and value follow with update and setValue.
  const shape = [id, options.name, options.label, options.mode, options.locale, options.required, options.inputClass].join('\u0000');

  const mounted = useRef<DatePickerController | null>(null);

  const ref = useCallback(
    (el: HTMLElement | null) => {
      mounted.current?.destroy();
      mounted.current = null;

      if (el) {
        mounted.current = createDatePicker(el, {
          ...latest.current,
          id,
          onChange: next => {
            setChange(next);
            latest.current.onChange?.(next);
          },
        });
      }

      setPicker(mounted.current);
    },
    [shape],
  );

  const presets = JSON.stringify(options.presets ?? []);

  useEffect(() => {
    picker?.update({ min: options.min ?? null, max: options.max ?? null, presets: JSON.parse(presets) });
  }, [picker, options.min, options.max, presets]);

  useEffect(() => {
    if (picker && options.value !== undefined && picker.value() !== options.value) picker.setValue(options.value);
  }, [picker, options.value]);

  return {
    ref,
    value: change.value,
    values: change.values,
    date: change.date,
    from: change.from,
    to: change.to,
    setValue: (value, index) => picker?.setValue(value, index),
    open: () => picker?.open(),
    close: () => picker?.close(),
    picker,
  };
}

export interface DatePickerProps extends UseDatePickerOptions {
  className?: string;
}

// DatePicker is useDatePicker in a div. value is controlled when given: the
// field follows it, and onChange reports what the user does.
export function DatePicker({ className, ...options }: DatePickerProps): ReactElement {
  const picker = useDatePicker(options);

  return createElement('div', { ref: picker.ref, className });
}

export type { DatePickerChange, DatePickerController } from './date-picker';
