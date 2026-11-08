# @thadeu/ui

A date picker that you do not have to write again: a positional mask, a calendar in a smart popover, a time row, presets and ranges. Plain TypeScript and the DOM, no framework, no dependency. ESM, CommonJS and types.

- **Mask:** `YYYY-MM-DD`, `YYYY-MM-DD HH:MM:SS`, `HH:MM:SS`. Digits fill their places and skip the separators; typing in the middle overwrites; a `4` as the first digit of a month becomes `04`; month, day (by the month and leap years), hour, minute and second stay in range; ArrowUp/ArrowDown step the part at the caret; a paste is read (`2026-11-12T14:04:05Z`, `2026-1-5 9:4`). Works with soft keyboards (`beforeinput`).
- **Popover:** in the native top layer (`popover` attribute), so a modal or a scroll box never cuts it. Below the field when it fits, above when there is more room there, moved back inside the viewport (`getBoundingClientRect`); a sheet on the bottom edge of a phone. Follows scroll and resize; Escape and a click outside close it.
- **Calendar:** Monday first, `min`/`max` (`max: "now"` follows the clock), arrow keys, month and weekday names from `Intl` (`locale`).
- **Time row** (datetime): `HH:MM:SS` with the same mask, Now and Done. A day click keeps the time.
- **Presets:** `{ label, ago: "15m" }` (an instant), `{ label, last: "7d" }` (a range), `{ label, at: "min" }`. Units: `s m h d w mo y`.
- **Range:** two fields, `{name}_from` and `{name}_to`; first click starts, second ends.
- **Without the script** it is a text field with a `pattern`, so a form still posts.
- All values are **UTC**.

## Install

```bash
npm install @thadeu/ui
```

```ts
import { createDatePicker } from '@thadeu/ui';
import '@thadeu/ui/date-picker.css';
```

## Use

In the browser:

```ts
const picker = createDatePicker(document.querySelector('#when')!, {
  id: 'at',
  name: 'at',
  label: 'At (UTC)',
  mode: 'datetime',                 // 'date' | 'datetime' | 'range'
  min: '2026-11-07T14:00:00Z',
  max: 'now',
  presets: [
    { label: '5 minutes ago', ago: '5m' },
    { label: '1 hour ago', ago: '1h' },
    { label: 'Start of the window', at: 'min' },
  ],
  inputClass: 'input',              // your field style
});

picker.values();                    // ['2026-11-08 14:04:05']
```

On a server (or any framework), write the markup and mount it in the browser:

```ts
// server: any template engine
import { datePickerHTML } from '@thadeu/ui';

html += datePickerHTML({ id: 'period', name: 'period', label: 'Period', mode: 'range', presets: [{ label: 'Last 7 days', last: '7d' }] });

// browser
import { mountDatePickers } from '@thadeu/ui';

mountDatePickers();                 // every [data-datepicker] on the page
```

The fields fire `change` when the picker writes them. To read a value as a `Date`, use `parse(value, 'datetime')`.

## Options

| Option | |
|---|---|
| `id`, `name`, `label` | required. A range posts `{name}_from` and `{name}_to`. |
| `mode` | `date`, `datetime` (default) or `range` |
| `value`, `from`, `to` | the first values, in the shape of the mask |
| `min`, `max` | RFC 3339 times; `max: "now"` |
| `presets` | see above |
| `required` | on the fields |
| `inputClass`, `inputAttrs` | your class and attributes on the fields |
| `locale` | for month and weekday names (`pt-BR`) |
| `labels` | `open`, `calendar`, `time`, `now`, `done`, `previous`, `next`, `hintDate`, `hintDatetime`, `hintRange` |

## Theme

Every color and size is a variable on `.datepicker`:

```css
.datepicker {
  --dp-accent: #2563eb;
  --dp-accent-ink: #fff;
  --dp-range: #dbeafe;
  --dp-radius: 6px;
  --dp-cell: 34px;
  --dp-font: 'Inter', sans-serif;
}
```

`--dp-bg --dp-ink --dp-muted --dp-border --dp-field-border --dp-accent --dp-accent-ink --dp-range --dp-hover --dp-disabled --dp-today --dp-radius --dp-radius-panel --dp-shadow --dp-cell --dp-font --dp-mono`.

## The parts

Each one is exported and pure where it can be, with tests:

| Module | |
|---|---|
| `date-mask` | `typeDigit`, `erase`, `step`, `normalize`, `parse`, `parseTime`, `format`, `pattern`, `template` |
| `calendar-model` | `monthGrid`, `addMonths`, `addDays`, `outside`, `clampDate`, `duration`, `resolvePreset`, `pickRange`, `inRange` |
| `popover-position` | `place(triggerRect, size, viewport, { prefer, gap, margin, sheetBelow })` |
| `popover` | `openPopover`, `closePopover`, `mountPopovers` (`[data-popover-open="id"]`) |
| `date-picker` | `datePickerHTML`, `mountDatePicker`, `mountDatePickers`, `createDatePicker`, `attachMask` |

## Develop

```bash
pnpm install
pnpm test          # bun test
pnpm typecheck
pnpm build         # dist: ESM, CJS, .d.ts, date-picker.css
```

It lives in the ndb console (`console/lib/ui`), which imports it as `@/lib/ui/...`.
