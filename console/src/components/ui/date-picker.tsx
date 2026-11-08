import { raw } from 'hono/html';
import type { FC } from 'hono/jsx';

import { datePickerHTML, mountDatePickers, type DatePickerOptions } from '@/lib/ui/date-picker';

import { NO_PASSWORD_MANAGER } from './input';

export type DatePickerProps = Omit<DatePickerOptions, 'inputClass' | 'inputAttrs'>;

// DatePicker is the date picker of lib/ui (a package of its own, for npm)
// with the input style of the console. The lib writes the markup, so the
// server and the browser agree on it.
export const DatePicker: FC<DatePickerProps> = props => raw(datePickerHTML({ ...props, inputClass: 'input mono', inputAttrs: NO_PASSWORD_MANAGER }));

export function mount(): void {
  mountDatePickers();
}
