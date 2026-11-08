# @tdu/ui

A date picker you do not write again. A positional mask, a calendar in a smart popover, a time row, presets and ranges. Vanilla DOM, React and Stimulus (Rails, Hotwire), no dependency, ESM + CommonJS + types.

```bash
npm install @tdu/ui
```

```ts
import '@tdu/ui/date-picker.css';
import { createDatePicker } from '@tdu/ui';          // vanilla
import { DatePicker, useDatePicker } from '@tdu/ui/react';   // React 18+
import { registerDatePicker } from '@tdu/ui/stimulus';        // Stimulus 3 (Rails, Hotwire)
```

- [What it does](#what-it-does)
- [Vanilla](#vanilla)
- [React](#react)
- [Rails and Hotwire](#rails-and-hotwire)
- [Server rendering](#server-rendering)
- [Options](#options)
- [The controller](#the-controller)
- [Events](#events)
- [Theme: CSS variables](#theme-css-variables)
- [Parts](#parts)

## What it does

| | |
|---|---|
| **Modes** | `date` (`YYYY-MM-DD`), `datetime` (`YYYY-MM-DD HH:MM:SS`), `range` (two dates) |
| **Mask** | digits fill their places and skip the separators; typing in the middle overwrites; a `4` as the first digit of a month becomes `04`; month, day (by the month and leap years), hour, minute and second stay in range; ArrowUp/ArrowDown step the part at the caret; a paste is read (`2026-11-12T14:04:05Z`, `2026-1-5 9:4`); soft keyboards work (`beforeinput`) |
| **Popover** | native top layer (`popover` attribute): a modal or a scroll box never cuts it. Below the field when it fits, above when there is more room, moved back inside the viewport (`getBoundingClientRect`). A sheet on the bottom edge of a phone (< 520 px). Follows scroll and resize; Escape and a click outside close it |
| **Calendar** | Monday first; `min` and `max` (`max: "now"` follows the clock); arrow keys; month and weekday names from `Intl` (`locale`) |
| **Time row** | in `datetime`: `HH:MM:SS` with the same mask, **Now** and **Done**. A day click keeps the time and the popover open; Enter or Done closes it |
| **Presets** | on the left: `{ label, ago: '15m' }` an instant, `{ label, last: '7d' }` a range (its start in single mode), `{ label, at: 'min' \| 'max' }`. Units `s m h d w mo y` |
| **Without JS** | a text field with a `pattern`: a form still posts |
| **Time zone** | every value is UTC |

## Vanilla

```ts
import { createDatePicker } from '@tdu/ui';
import '@tdu/ui/date-picker.css';

const picker = createDatePicker(document.querySelector('#when')!, {
  id: 'at',
  name: 'at',
  label: 'At (UTC)',
  mode: 'datetime',
  min: '2026-11-07T14:00:00Z',
  max: 'now',
  presets: [
    { label: '5 minutes ago', ago: '5m' },
    { label: '1 hour ago', ago: '1h' },
    { label: 'Start of the window', at: 'min' },
  ],
  onChange: change => console.log(change.value, change.date),
});

picker.value();                                  // '2026-11-08 14:04:05'
picker.date();                                   // Date (UTC), or null while typing
picker.setValue(new Date());                     // fires a change
picker.update({ min: '2026-11-08T00:00:00Z' });
picker.destroy();                                // removes the listeners
```

## React

`useDatePicker` gives a `ref` for a container and the state. The picker owns the DOM of that container; React renders an empty element and never touches its children.

```tsx
import { useDatePicker } from '@tdu/ui/react';

function RestoreForm() {
  const at = useDatePicker({ name: 'at', label: 'At (UTC)', max: 'now', presets: [{ label: '1 hour ago', ago: '1h' }] });

  return (
    <form>
      <div ref={at.ref} />
      <button disabled={!at.date}>Restore to {at.value}</button>
    </form>
  );
}
```

`<DatePicker>` is the hook in a `div`. With `value` it is controlled: the field follows the prop, and `onChange` reports what the user does.

```tsx
import { DatePicker } from '@tdu/ui/react';

function Filter() {
  const [from, setFrom] = useState('2026-11-01');

  return <DatePicker name="from" label="From" mode="date" value={from} onChange={c => setFrom(c.value)} className="filter" />;
}
```

| `useDatePicker(options)` returns | |
|---|---|
| `ref` | put it on the container element |
| `value`, `values` | the fields as typed (`values[1]` is the end of a range) |
| `date`, `from`, `to` | parsed, `null` while a field is not complete |
| `setValue(value, index?)` | a `Date`, a text in the shape of the mask, or `null` |
| `open()`, `close()` | the popover |
| `picker` | the vanilla [controller](#the-controller), once mounted |

- `id` is optional (it comes from `useId`). Changing `name`, `label`, `mode`, `locale`, `required` or `inputClass` makes the picker again; `min`, `max`, `presets` and `value` update it in place.
- StrictMode is fine: the double mount makes one picker.
- In the Next.js App Router, use it in a client component (`'use client'`).

## Rails and Hotwire

The Stimulus controller grows the picker around the input that the form builder writes, so the input keeps its `name`, `value`, `id`, `<label>` and validations. Put `data-controller="tdu-datepicker"` on an element that holds the input (two inputs make a range).

```erb
<%= form_with model: @restore do |form| %>
  <div data-controller="tdu-datepicker"
       data-tdu-datepicker-max-value="now"
       data-tdu-datepicker-presets-value="<%= [{ label: "1 hour ago", ago: "1h" }].to_json %>">
    <%= form.label :at, "Restore at (UTC)" %>
    <%= form.text_field :at %>
  </div>

  <div data-controller="tdu-datepicker">            <%# date_field: mode date %>
    <%= form.date_field :day %>
  </div>

  <div data-controller="tdu-datepicker" data-tdu-datepicker-locale-value="pt-BR">
    <%= form.text_field :from %> <%= form.text_field :to %>   <%# two inputs: a range %>
  </div>
<% end %>
```

### Install

```bash
bin/importmap pin @tdu/ui/stimulus        # importmap-rails
# or: yarn add @tdu/ui / npm install @tdu/ui (jsbundling-rails)
```

```js
// app/javascript/controllers/index.js
import { application } from "controllers/application"
import { DatePickerController } from "@tdu/ui/stimulus"

application.register("tdu-datepicker", DatePickerController)
```

The CSS: `@import "@tdu/ui/date-picker.css";` with cssbundling-rails, or copy `dist/date-picker.css` to `app/assets/stylesheets/`. Theme it with the [variables](#theme-css-variables), next to your Tailwind or CSS.

### Values, actions, events

| `data-tdu-datepicker-*-value` | |
|---|---|
| `mode` | `date`, `datetime`, `range`. Default: `range` with two inputs, `date` for `date_field`, else `datetime` |
| `min`, `max` | RFC 3339, `max: now`. A change updates the open picker |
| `presets` | JSON array (`.to_json`) |
| `locale`, `label` | month names; the accessible name (default: the `<label>` text) |
| `labels` | JSON object, to translate the buttons and hints |

- **Actions:** `click->tdu-datepicker#open`, `#close`, `#clear`.
- **Event:** `tdu-datepicker:change` with the [change detail](#events): `data-action="tdu-datepicker:change->filters#submit"`.
- **Outlets:** another controller reads `value` and `date` of the `tdu-datepicker` outlet.
- `date_field` and `datetime_field` become text fields with the mask (`2026-11-05T09:30` is read as `2026-11-05 09:30:00`); disconnect gives them back as they were.

### Turbo

- **Drive and the cache:** disconnect restores the markup before Turbo caches the page; a snapshot that still has an old picker is cleaned when it comes back, never doubled.
- **Frames and Streams:** Stimulus connects the new elements.
- **Without Stimulus:** mark the element and observe once:

  ```erb
  <div data-tdu-datepicker="date" data-tdu-datepicker-max="now"><%= form.text_field :day %></div>
  ```

  ```js
  import { observeDatePickers } from "@tdu/ui"

  observeDatePickers()     // now, and every element that Turbo adds or removes later
  ```

  The attributes are `data-tdu-datepicker` (the mode, or empty), `-min`, `-max`, `-locale`, `-label`, `-presets` and `-labels` (JSON).

### Time zone

The values are UTC (`2026-11-08 14:04:05`). With `config.time_zone = "UTC"` (the Rails default) `params[:at]` parses as it is. With another zone, read it as UTC: `Time.find_zone("UTC").parse(params[:at])`.

### A helper

```ruby
# app/helpers/date_picker_helper.rb
module DatePickerHelper
  def date_picker(form, attribute, max: "now", presets: [], **options)
    data = { controller: "tdu-datepicker", tdu_datepicker_max_value: max, tdu_datepicker_presets_value: presets.to_json }

    tag.div(data:) { form.text_field(attribute, **options) }
  end
end
```

```erb
<%= date_picker form, :at, presets: [{ label: "15 minutes ago", ago: "15m" }] %>
```

## Server rendering

Write the markup on the server (any template, any framework), mount it in the browser. Before the script runs it is a plain field that posts.

```ts
// server
import { datePickerHTML } from '@tdu/ui';

const html = datePickerHTML({ id: 'period', name: 'period', label: 'Period', mode: 'range', presets: [{ label: 'Last 7 days', last: '7d' }] });

// browser
import { mountDatePickers } from '@tdu/ui';

mountDatePickers(document, { onChange: change => console.log(change.from, change.to) });
```

A range posts `{name}_from` and `{name}_to`.

## Options

| Option | Type | |
|---|---|---|
| `id` | `string` | required (optional in React). The fields get `id` (and `id-to`), the popover `id-calendar` |
| `name` | `string` | required. A range posts `{name}_from` and `{name}_to` |
| `label` | `string` | required: the accessible name of the field and the button |
| `mode` | `'date' \| 'datetime' \| 'range'` | default `datetime` |
| `value`, `from`, `to` | `string` | first values, in the shape of the mask |
| `min`, `max` | `string` | RFC 3339; `max: 'now'` follows the clock |
| `presets` | `Preset[]` | `{ label, ago }`, `{ label, last }`, `{ label, at }` |
| `required` | `boolean` | on the fields |
| `inputClass` | `string` | your field class, added to `datepicker-input` |
| `inputAttrs` | `Record<string, string \| boolean>` | more attributes on the fields (`data-1p-ignore: true`) |
| `locale` | `string` | month and weekday names (`'pt-BR'`) |
| `labels` | `Partial<DatePickerLabels>` | `open`, `calendar`, `time`, `now`, `done`, `previous`, `next`, `hintDate`, `hintDatetime`, `hintRange` |
| `onChange` | `(change) => void` | every change, typed or picked |

## The controller

`createDatePicker`, `mountDatePicker` and `mountDatePickers` return a `DatePickerController`:

| | |
|---|---|
| `root` | the `.datepicker` element |
| `value()`, `values()` | the fields as typed |
| `date()` | the first field as a `Date`, or `null` |
| `setValue(value, index = 0)` | `Date`, mask text or `null`; fires a change |
| `update({ min?, max?, presets? })` | `null` removes a bound |
| `open()`, `close()` | the popover |
| `destroy()` | removes every listener; the markup stays |

`mountDatePicker` on a root that is mounted already returns the same controller.

## Events

Every change calls `onChange` and dispatches a bubbling `datepicker:change` on the root, with the same detail:

```ts
interface DatePickerChange {
  value: string;        // first field, as typed
  values: string[];
  date: Date | null;    // first field parsed
  from: Date | null;    // range only
  to: Date | null;      // range only
}

document.addEventListener('datepicker:change', event => {
  const { value, date } = (event as CustomEvent<DatePickerChange>).detail;
});
```

The fields also fire a native `change` when the picker writes them, so a form library that listens to inputs sees it.

## Theme: CSS variables

Set them on `.datepicker` (one picker), on a parent, or on `:root` (all of them). The popover is in the top layer but inherits them from its picker.

| Variable | Default | Changes |
|---|---|---|
| **Colors** | | |
| `--dp-bg` | `#ffffff` | panel background |
| `--dp-ink` | `#0b0b0f` | text |
| `--dp-muted` | `#6b6b76` | weekdays, hints, days of other months, arrows |
| `--dp-border` | `#e5e5e5` | panel border and the lines inside it |
| `--dp-field-bg` | `var(--dp-bg)` | the fields, the calendar button, the time field |
| `--dp-field-ink` | `var(--dp-ink)` | text of the fields |
| `--dp-field-border` | `#d9d9de` | border of the fields and buttons |
| `--dp-accent` | `#f0047f` | selected day, focus, hover of the button, Done |
| `--dp-accent-ink` | `#ffffff` | text on the accent |
| `--dp-range` | `#fde7f2` | days inside a range; the focus ring of the fields |
| `--dp-range-ink` | `var(--dp-ink)` | text of the days inside a range |
| `--dp-hover` | `#f2f2f4` | hover of days, presets and buttons |
| `--dp-disabled` | `#b8b8c0` | days out of `min`/`max` |
| `--dp-today` | `var(--dp-ink)` | the ring around today |
| **Shape** | | |
| `--dp-radius` | `8px` | fields and buttons |
| `--dp-day-radius` | `var(--dp-radius)` | a day (`50%` for round days) |
| `--dp-radius-panel` | `14px` | the popover |
| `--dp-border-width` | `1px` | every border |
| **Size and space** | | |
| `--dp-cell` | `36px` (`40px` on a phone) | a day, width and height |
| `--dp-gap` | `2px` | between days |
| `--dp-padding` | `12px` | inside the panel |
| `--dp-field-height` | `40px` | the fields and the calendar button |
| `--dp-control-height` | `34px` | the time field, Now, Done |
| `--dp-presets-width` | `150px` | the presets column |
| **Type** | | |
| `--dp-font` | `inherit` | panel text |
| `--dp-mono` | `'JetBrains Mono', ui-monospace, monospace` | fields and days |
| `--dp-font-size` | `13px` | days, presets, buttons |
| `--dp-title-size` | `14px` | month and year |
| `--dp-small-size` | `12px` | hints, the time label |
| `--dp-weekday-size` | `11px` | weekday names |
| `--dp-weekday-case` | `uppercase` | `none` keeps `Mon` |
| **Effects** | | |
| `--dp-shadow` | `0 12px 40px rgba(0,0,0,.14)` | the popover |
| `--dp-z` | `50` | z-index, only where the native top layer is missing |

A blue theme with round days:

```css
.datepicker {
  --dp-accent: #2563eb;
  --dp-range: #dbeafe;
  --dp-day-radius: 50%;
  --dp-font: 'Inter', sans-serif;
}
```

Dark:

```css
@media (prefers-color-scheme: dark) {
  :root {
    --dp-bg: #15151a;
    --dp-ink: #f4f4f6;
    --dp-muted: #9a9aa5;
    --dp-border: #2a2a31;
    --dp-field-border: #3a3a42;
    --dp-accent: #7c3aed;
    --dp-range: #2e1f4d;
    --dp-hover: #24242b;
    --dp-disabled: #55555e;
  }
}
```

Your own field style: pass `inputClass: 'input'`; your class comes after `datepicker-input`, so it wins where it sets the same property.

### Class names

`.datepicker`, `.datepicker-field`, `.datepicker-input`, `.datepicker-sep`, `.datepicker-toggle`, `.popover-layer` (with `data-placement`: `bottom-start`, `bottom-end`, `top-start`, `top-end`, `sheet`), `.dp-panel`, `.dp-presets`, `.dp-preset`, `.dp-main`, `.dp-head`, `.dp-title`, `.dp-nav`, `.dp-grid`, `.dp-weekday`, `.dp-day` (`.out`, `.today`, `.on`, `.in-range`, `:disabled`), `.dp-time`, `.dp-time-label`, `.dp-time-input`, `.dp-btn` (`.primary`), `.dp-hint`.

## Parts

Each part is exported, pure where it can be, and tested:

| Module | |
|---|---|
| `date-mask` | `typeDigit`, `erase`, `step`, `normalize`, `parse`, `parseTime`, `format`, `pattern`, `template` (`date`, `datetime`, `time`) |
| `calendar-model` | `monthGrid`, `addMonths`, `addDays`, `outside`, `clampDate`, `duration`, `resolvePreset`, `pickRange`, `inRange` |
| `popover-position` | `place(triggerRect, size, viewport, { prefer, gap, margin, sheetBelow })` |
| `popover` | `openPopover`, `closePopover`, `repositionPopover`, `mountPopovers` (`[data-popover-open="id"]`) |
| `date-picker` | `datePickerHTML`, `mountDatePicker`, `mountDatePickers`, `createDatePicker`, `attachMask`, `DEFAULT_LABELS` |
| `enhance` | `enhance(root, options)` (existing inputs, returns a controller with `restore()`), `observeDatePickers()`, `optionsOf(el)` |
| `stimulus` (`@tdu/ui/stimulus`) | `DatePickerController`, `registerDatePicker(application, identifier?)` |
| `react` (`@tdu/ui/react`) | `useDatePicker`, `DatePicker` |

```ts
import { attachMask } from '@tdu/ui';

attachMask(document.querySelector('input')!, 'time');   // just the mask
```

## Develop

```bash
pnpm install
pnpm test          # bun test
pnpm typecheck
pnpm build         # dist: index, react and stimulus, ESM + CJS + .d.ts, date-picker.css
```

It lives in the ndb console (`console/lib/ui`), which imports it as `@/lib/ui/...`. License MIT.
