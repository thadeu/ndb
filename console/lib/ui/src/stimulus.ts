// The Stimulus controller of the date picker, for Rails and Hotwire apps.
//
//   import { Application } from '@hotwired/stimulus';
//   import { registerDatePicker } from '@tdu/ui/stimulus';
//
//   registerDatePicker(Application.start());   // data-controller="tdu-datepicker"
//
//   <div data-controller="tdu-datepicker" data-tdu-datepicker-max-value="now">
//     <%= form.text_field :at %>
//   </div>
//
// The controller element holds the input (two for a range); the form
// builder keeps its name, value and validations. Disconnect restores the
// markup, so Turbo caches a clean page.

import { Controller, type Application } from '@hotwired/stimulus';

import type { Preset } from './calendar-model';
import type { DatePickerChange, DatePickerLabels, DatePickerMode } from './date-picker';
import { enhance, type EnhancedDatePicker } from './enhance';

export class DatePickerController extends Controller<HTMLElement> {
  static values = {
    mode: String,
    min: String,
    max: String,
    locale: String,
    label: String,
    presets: Array,
    labels: Object,
  };

  declare readonly modeValue: string;
  declare readonly minValue: string;
  declare readonly maxValue: string;
  declare readonly localeValue: string;
  declare readonly labelValue: string;
  declare readonly presetsValue: Preset[];
  declare readonly labelsValue: Partial<DatePickerLabels>;

  picker?: EnhancedDatePicker;

  connect(): void {
    this.picker = enhance(this.element, {
      mode: (this.modeValue || undefined) as DatePickerMode | undefined,
      min: this.minValue || undefined,
      max: this.maxValue || undefined,
      locale: this.localeValue || undefined,
      label: this.labelValue || undefined,
      presets: this.presetsValue,
      labels: this.labelsValue,
      onChange: change => this.dispatch('change', { detail: change }),
    });
  }

  disconnect(): void {
    this.picker?.restore();
    this.picker = undefined;
  }

  minValueChanged(): void {
    this.picker?.update({ min: this.minValue || null });
  }

  maxValueChanged(): void {
    this.picker?.update({ max: this.maxValue || null });
  }

  presetsValueChanged(): void {
    this.picker?.update({ presets: this.presetsValue });
  }

  // Actions: data-action="click->tdu-datepicker#open"
  open(): void {
    this.picker?.open();
  }

  close(): void {
    this.picker?.close();
  }

  clear(): void {
    this.picker?.setValue(null);

    if (this.picker?.values().length === 2) this.picker.setValue(null, 1);
  }

  // The current values, for another controller (an outlet).
  get value(): string {
    return this.picker?.value() ?? '';
  }

  get date(): Date | null {
    return this.picker?.date() ?? null;
  }
}

// registerDatePicker registers the controller, as tdu-datepicker by default.
export function registerDatePicker(application: Application, identifier = 'tdu-datepicker'): void {
  application.register(identifier, DatePickerController);
}

export type { DatePickerChange };
export default DatePickerController;
