import type { FC } from 'hono/jsx';

import { type KeyContext, applyCompletion, editorRows, errorText, formatJson, highlight, jsonError } from '@/components/code-editor-model';
import { type Edit, editKey } from '@/components/editor-keys';

import { noSpelling } from './input';

export interface CodeEditorProps {
  value: string;
  label: string;
  class?: string;
  [attribute: string]: unknown;
}

// CodeEditor is a JSON editor in the dark theme of Dev Tools. The server
// renders a plain textarea, so a form works without the script. The script
// puts a colored layer and line numbers under the text, handles the keys
// (Tab, brackets, Enter), formats the JSON, and says when it is not valid.
// Other attributes, such as name and readonly, go to the textarea.
export const CodeEditor: FC<CodeEditorProps> = ({ value, label, class: extra, ...rest }) => {
  const className = ['ce', extra ?? ''].filter(Boolean).join(' ');

  return (
    <div class={className} data-ce>
      <div class="ce-box">
        <div class="ce-scroll">
          <div class="ce-gutter" aria-hidden="true" />

          <div class="ce-main">
            <pre class="ce-hl" aria-hidden="true" />

            <textarea
              class="ce-ta"
              rows={editorRows(value)}
              wrap="off"
              aria-label={label}
              {...noSpelling}
              {...rest}
            >
              {value}
            </textarea>
          </div>
        </div>

        <div class="ce-foot" hidden>
          <span class="ce-keys">Tab indents. Ctrl or Cmd + Enter runs.</span>

          <button type="button" class="ed-btn ce-btn" data-ce-format>
            Format
          </button>
        </div>
      </div>

      <p class="ce-error" aria-live="polite" hidden data-ce-error />
    </div>
  );
};

export interface Completion {
  name: string;
  what?: string;
}

export interface Suggestion {
  context: KeyContext;
  items: Completion[];
}

export type Suggest = (text: string, caret: number) => Suggestion | undefined;

interface Editor {
  host: HTMLElement;
  box: HTMLElement;
  scroll: HTMLElement;
  main: HTMLElement;
  area: HTMLTextAreaElement;
  list: HTMLUListElement;
  suggestion?: Suggestion;
  active: number;
  // After Escape, the next Tab leaves the editor, for keyboard users.
  escaped: boolean;
}

const editors = new WeakMap<HTMLElement, Editor>();
const suggesters = new WeakMap<HTMLElement, Suggest>();

// configure gives an editor the function that offers completions at the
// caret. It can run before or after the editor is mounted.
export function configure(host: HTMLElement, options: { suggest: Suggest }): void {
  suggesters.set(host, options.suggest);
}

// paint draws the colored layer, the line numbers and the error line.
function paint(host: HTMLElement): void {
  const area = host.querySelector<HTMLTextAreaElement>('.ce-ta');
  const layer = host.querySelector<HTMLElement>('.ce-hl');
  const gutter = host.querySelector<HTMLElement>('.ce-gutter');
  const errorLine = host.querySelector<HTMLElement>('[data-ce-error]');

  if (!area || !layer || !gutter) return;

  const lines = highlight(area.value).map(tokens => {
    const line = document.createElement('div');

    line.className = 'ce-line';

    for (const token of tokens) {
      const part = document.createElement('span');

      part.className = `tok-${token.tone}`;
      part.textContent = token.text;

      line.append(part);
    }

    if (tokens.length === 0) line.textContent = '​';

    return line;
  });

  layer.replaceChildren(...lines);
  gutter.textContent = lines.map((_, index) => index + 1).join('\n');

  const error = jsonError(area.value);

  host.classList.toggle('bad', error !== undefined);

  if (errorLine) {
    errorLine.hidden = error === undefined;
    errorLine.textContent = error ? errorText(error) : '';
  }
}

// apply writes an edit and tells the form with an input event, as a key of
// the user does.
function apply(editor: Editor, edit: Edit): void {
  editor.area.value = edit.text;
  editor.area.setSelectionRange(edit.start, edit.end);

  editor.area.dispatchEvent(new Event('input', { bubbles: true }));
}

function closeList(editor: Editor): void {
  editor.suggestion = undefined;
  editor.active = 0;
  editor.list.hidden = true;
}

function charWidth(editor: Editor): number {
  const probe = document.createElement('span');

  probe.textContent = '0'.repeat(20);
  probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;font:inherit';

  editor.main.append(probe);

  const width = probe.getBoundingClientRect().width / 20;

  probe.remove();

  return width;
}

// place puts the list under the key that the user is typing.
function place(editor: Editor, context: KeyContext): void {
  const { area, box, main, scroll, list } = editor;
  const style = getComputedStyle(area);

  const before = area.value.slice(0, context.from);
  const line = before.split('\n').length;
  const column = before.length - before.lastIndexOf('\n') - 1;

  const top = main.offsetTop + line * parseFloat(style.lineHeight) - scroll.scrollTop;
  const left = main.offsetLeft + parseFloat(style.paddingLeft) + column * charWidth(editor) - scroll.scrollLeft;
  const room = box.clientWidth - list.offsetWidth - 8;

  list.style.top = `${top + 4}px`;
  list.style.left = `${Math.max(8, Math.min(left, room))}px`;
}

function paintList(editor: Editor): void {
  const items = editor.suggestion?.items ?? [];

  const rows = items.map((item, index) => {
    const row = document.createElement('li');
    const name = document.createElement('span');
    const what = document.createElement('span');

    name.className = 'ce-option-name';
    name.textContent = item.name;

    what.className = 'ce-option-what';
    what.textContent = item.what ?? '';

    row.className = index === editor.active ? 'ce-option on' : 'ce-option';
    row.setAttribute('role', 'option');
    row.setAttribute('aria-selected', String(index === editor.active));
    row.dataset.index = String(index);
    row.append(name, what);

    return row;
  });

  editor.list.replaceChildren(...rows);
  editor.list.querySelector('.on')?.scrollIntoView({ block: 'nearest' });
}

// refresh opens, updates or closes the list for the text at the caret.
function refresh(editor: Editor): void {
  const suggest = suggesters.get(editor.host);
  const caret = editor.area.selectionStart;
  const suggestion = suggest && editor.area.selectionEnd === caret ? suggest(editor.area.value, caret) : undefined;

  if (!suggestion) {
    closeList(editor);

    return;
  }

  editor.suggestion = suggestion;
  editor.active = Math.min(editor.active, suggestion.items.length - 1);
  editor.list.hidden = false;

  paintList(editor);
  place(editor, suggestion.context);
}

function accept(editor: Editor, index: number): void {
  const item = editor.suggestion?.items[index];

  if (!item || !editor.suggestion) return;

  const edit = applyCompletion(editor.area.value, editor.suggestion.context, item.name);

  closeList(editor);
  apply(editor, edit);
}

// listKey handles a key while the list is open. It returns true when the key
// is used.
function listKey(editor: Editor, event: KeyboardEvent): boolean {
  const count = editor.suggestion?.items.length ?? 0;

  switch (event.key) {
    case 'ArrowDown':
    case 'ArrowUp':
      editor.active = (editor.active + (event.key === 'ArrowDown' ? 1 : -1) + count) % count;
      paintList(editor);

      return true;

    case 'Enter':
    case 'Tab':
      accept(editor, editor.active);

      return true;

    case 'Escape':
      closeList(editor);

      return true;

    default:
      return false;
  }
}

function onKey(editor: Editor, event: KeyboardEvent): void {
  const { area } = editor;
  const command = event.metaKey || event.ctrlKey;

  if (editor.suggestion && !command && listKey(editor, event)) {
    event.preventDefault();

    return;
  }

  if (command && event.key === 'Enter') {
    event.preventDefault();
    area.form?.requestSubmit();

    return;
  }

  if (event.key === 'Escape') {
    editor.escaped = true;

    return;
  }

  const leave = event.key === 'Tab' && editor.escaped;

  editor.escaped = false;

  if (leave || area.readOnly || command || event.altKey || event.isComposing) return;

  const edit = editKey(area.value, area.selectionStart, area.selectionEnd, { key: event.key, shift: event.shiftKey });

  if (!edit) return;

  event.preventDefault();
  apply(editor, edit);
}

function format(editor: Editor): void {
  const formatted = formatJson(editor.area.value);

  editor.area.focus();

  if (formatted === undefined || formatted === editor.area.value) return;

  apply(editor, { text: formatted, start: formatted.length, end: formatted.length });
}

// setValue replaces the text of an editor, mounted or not.
export function setValue(host: HTMLElement, text: string): void {
  const area = host.querySelector<HTMLTextAreaElement>('.ce-ta');

  if (!area) return;

  area.value = text;

  if (editors.has(host)) paint(host);
}

// upgrade turns the plain textarea of one editor into the full editor.
export function upgrade(host: HTMLElement): void {
  const box = host.querySelector<HTMLElement>('.ce-box');
  const scroll = host.querySelector<HTMLElement>('.ce-scroll');
  const main = host.querySelector<HTMLElement>('.ce-main');
  const area = host.querySelector<HTMLTextAreaElement>('.ce-ta');
  const foot = host.querySelector<HTMLElement>('.ce-foot');

  if (!box || !scroll || !main || !area || editors.has(host)) return;

  const list = document.createElement('ul');

  list.className = 'ce-list';
  list.hidden = true;
  list.setAttribute('role', 'listbox');
  list.setAttribute('aria-label', 'Operators');

  box.append(list);

  const editor: Editor = { host, box, scroll, main, area, list, active: 0, escaped: false };

  editors.set(host, editor);
  host.classList.add('on');

  if (foot) foot.hidden = area.readOnly;

  paint(host);

  area.addEventListener('input', () => {
    paint(host);
    refresh(editor);
  });

  area.addEventListener('keydown', event => onKey(editor, event));
  area.addEventListener('click', () => closeList(editor));
  area.addEventListener('blur', () => closeList(editor));
  scroll.addEventListener('scroll', () => closeList(editor));

  // The mouse must not take the focus from the text, or the list closes
  // before the click.
  list.addEventListener('mousedown', event => event.preventDefault());

  list.addEventListener('click', event => {
    const row = event.target instanceof Element ? event.target.closest<HTMLElement>('.ce-option') : null;

    if (row) accept(editor, Number(row.dataset.index));
  });

  foot?.querySelector('[data-ce-format]')?.addEventListener('click', () => format(editor));
}

export function mount(): void {
  for (const host of document.querySelectorAll<HTMLElement>('[data-ce]')) upgrade(host);
}
