// The browser part of the Aggregate page. The server renders a plain form
// (the pipeline as JSON in one textarea, links that add and remove a stage).
// This script edits the stages in place: each stage body becomes a code
// editor that offers the operators of its stage, the stages move up and
// down, and the request card and the hidden JSON follow each change. It also
// counts the collections of a list or a pattern, and keeps the saved
// pipelines in localStorage.
import { type Stage, aggregatePath, formatPipeline, isMultiTarget, requestBody, suggestOperators } from '@/aggregate';
import { configure, setValue, upgrade as upgradeEditor } from '@/ui/code-editor';

import { buildCurl } from './curl';

interface Saved {
  name: string;
  on: string;
  pipeline: string;
}

const KEY = 'ndb-ui:pipelines:';

function readSaved(name: string): Saved[] {
  try {
    const value = JSON.parse(localStorage.getItem(KEY + name) ?? '[]');

    return Array.isArray(value) ? (value as Saved[]) : [];
  } catch {
    return [];
  }
}

function writeSaved(name: string, list: Saved[]): void {
  try {
    localStorage.setItem(KEY + name, JSON.stringify(list));
  } catch {
    // Storage can be blocked. The list then lives until the page closes.
  }
}

const MATCH_DELAY_MS = 250;

function setDisabled(button: Element | null, disabled: boolean): void {
  if (disabled) button?.setAttribute('aria-disabled', 'true');
  else button?.removeAttribute('aria-disabled');
}

// upgrade opens the read-only body of a stage card for edits, and gives its
// editor the operators of the stage.
function upgrade(card: HTMLElement, text?: string): void {
  const editor = card.querySelector<HTMLElement>('[data-ce]');
  const area = card.querySelector<HTMLTextAreaElement>('textarea[data-agg-body]');

  if (!editor || !area) return;

  area.readOnly = false;
  area.setAttribute('aria-label', `Body of the stage ${card.dataset.op ?? ''}`);

  if (text !== undefined) setValue(editor, text);

  configure(editor, { suggest: (body, caret) => suggestOperators(card.dataset.op ?? '', body, caret) });
  upgradeEditor(editor);
}

// mountMatch shows, under the target field, how many collections a list or
// a pattern names. The server asks ndb.
function mountMatch(form: HTMLFormElement, on: HTMLInputElement): void {
  const line = form.querySelector<HTMLElement>('[data-agg-match]');
  const action = form.getAttribute('action') ?? '';

  if (!line) return;

  let timer: ReturnType<typeof setTimeout> | undefined;
  let asked = on.value.trim();

  const show = (text: string, bad: boolean) => {
    line.textContent = text;
    line.hidden = text === '';
    line.classList.toggle('bad', bad);
  };

  const ask = async (target: string) => {
    try {
      const response = await fetch(`${action}/match?${new URLSearchParams({ on: target })}`, { headers: { accept: 'application/json' } });
      const answer = (await response.json()) as { text?: string; bad?: boolean };

      if (asked === target) show(answer.text ?? '', answer.bad === true);
    } catch {
      if (asked === target) show('', false);
    }
  };

  on.addEventListener('input', () => {
    const target = on.value.trim();

    if (target === asked) return;

    asked = target;
    clearTimeout(timer);

    if (!isMultiTarget(target)) {
      show('', false);

      return;
    }

    timer = setTimeout(() => ask(target), MATCH_DELAY_MS);
  });
}

function mountForm(form: HTMLFormElement): void {
  const list = form.querySelector<HTMLElement>('[data-agg-stages]');
  const raw = form.querySelector<HTMLTextAreaElement>('[data-agg-raw]');
  const on = form.querySelector<HTMLInputElement>('[data-agg-on]');
  const template = document.querySelector<HTMLTemplateElement>('template[data-agg-template]');

  if (!list || !raw || !on || !template) return;

  const errorBox = form.querySelector<HTMLElement>('[data-agg-error]');
  const pathText = form.querySelector<HTMLElement>('[data-agg-path]');
  const jsonText = form.querySelector<HTMLElement>('[data-agg-json]');
  const curlButton = form.querySelector<HTMLElement>('[data-agg-curl]');

  const cards = () => [...list.querySelectorAll<HTMLElement>('[data-agg-stage]')];

  // number writes the place of each card, and turns off the move button
  // that has no place to go.
  function number(): void {
    const all = cards();

    for (const [index, card] of all.entries()) {
      const label = card.querySelector('[data-agg-num]');

      if (label) label.textContent = String(index + 1);

      setDisabled(card.querySelector('[data-agg-move="up"]'), index === 0);
      setDisabled(card.querySelector('[data-agg-move="down"]'), index === all.length - 1);
    }
  }

  // read gives the stages of the cards, or undefined when a body is not JSON.
  function read(): Stage[] | undefined {
    const stages: Stage[] = [];
    let valid = true;

    number();

    for (const card of cards()) {
      const area = card.querySelector<HTMLTextAreaElement>('textarea[data-agg-body]');

      try {
        stages.push({ op: card.dataset.op ?? '', body: JSON.parse(area?.value ?? '') });
        card.classList.remove('bad');
      } catch {
        card.classList.add('bad');
        valid = false;
      }
    }

    return valid ? stages : undefined;
  }

  function sync(edited: boolean): Stage[] | undefined {
    const stages = read();

    if (errorBox) {
      errorBox.hidden = stages !== undefined;
      errorBox.textContent = stages ? '' : 'A stage body is not valid JSON. Correct the stage with the red line.';
    }

    if (edited) {
      for (const out of list!.querySelectorAll('[data-agg-out]')) out.textContent = '';
    }

    if (!stages) return undefined;

    const path = aggregatePath(on!.value.trim());
    const body = requestBody(stages);

    raw!.value = formatPipeline(stages);

    if (pathText) pathText.textContent = path;
    if (jsonText) jsonText.textContent = body;
    if (curlButton) curlButton.dataset.copy = buildCurl({ method: 'POST', path, body, socket: form.dataset.socket, base: form.dataset.base });

    return stages;
  }

  function add(chip: HTMLElement): void {
    const card = template!.content.querySelector<HTMLElement>('[data-agg-stage]')?.cloneNode(true) as HTMLElement | undefined;

    if (!card) return;

    const op = card.querySelector('[data-agg-op]');
    const what = card.querySelector('[data-agg-what]');

    card.dataset.op = chip.dataset.op ?? '';

    if (op) op.textContent = chip.dataset.op ?? '';
    if (what) what.textContent = chip.dataset.what ?? '';

    list!.append(card);
    upgrade(card, chip.dataset.sample ?? '{}');

    sync(true);

    card.querySelector('textarea')?.focus();
  }

  // move puts a card before the one above it, or after the one below it.
  function move(button: HTMLElement): void {
    const card = button.closest<HTMLElement>('[data-agg-stage]');
    const other = button.dataset.aggMove === 'up' ? card?.previousElementSibling : card?.nextElementSibling;

    if (!card || !other) return;

    if (button.dataset.aggMove === 'up') other.before(card);
    else other.after(card);

    sync(true);

    const same = card.querySelector<HTMLElement>(`[data-agg-move="${button.dataset.aggMove}"]`);
    const target = same?.hasAttribute('aria-disabled') ? card.querySelector<HTMLElement>('[data-agg-move]:not([aria-disabled])') : same;

    target?.focus();
  }

  for (const card of cards()) upgrade(card);

  const rawBox = form.querySelector<HTMLElement>('[data-agg-rawbox]');

  if (rawBox) rawBox.hidden = true;

  form.addEventListener('input', event => {
    const target = event.target;

    if (target instanceof HTMLTextAreaElement && target.dataset.aggBody !== undefined) sync(true);

    if (target === on) sync(false);
  });

  form.addEventListener('click', event => {
    const target = event.target instanceof Element ? event.target : null;
    const chip = target?.closest<HTMLElement>('[data-agg-add]');
    const remove = target?.closest<HTMLElement>('[data-agg-remove]');
    const mover = target?.closest<HTMLElement>('[data-agg-move]');

    if (mover) {
      event.preventDefault();
      move(mover);

      return;
    }

    if (chip) {
      event.preventDefault();
      add(chip);

      return;
    }

    if (remove) {
      event.preventDefault();
      remove.closest('[data-agg-stage]')?.remove();

      sync(true);
    }
  });

  form.addEventListener('submit', event => {
    if (!sync(false)) event.preventDefault();
  });

  sync(false);

  mountMatch(form, on);
  mountSaved(form, raw, on);
}

function mountSaved(form: HTMLFormElement, raw: HTMLTextAreaElement, on: HTMLInputElement): void {
  const opener = form.querySelector<HTMLElement>('[data-agg-saved-open]');
  const listBox = document.querySelector<HTMLElement>('[data-agg-saved-list]');
  const empty = document.querySelector<HTMLElement>('[data-agg-saved-empty]');
  const nameInput = document.querySelector<HTMLInputElement>('[data-agg-saved-name]');
  const save = document.querySelector<HTMLElement>('[data-agg-saved-save]');

  if (!opener || !listBox || !nameInput || !save) return;

  const connection = form.dataset.aggName ?? '';
  const action = form.getAttribute('action') ?? '';

  function paint(): void {
    const saved = readSaved(connection);

    listBox!.replaceChildren();

    if (empty) empty.hidden = saved.length > 0;

    for (const item of saved) {
      const row = document.createElement('li');
      const link = document.createElement('a');
      const target = document.createElement('span');
      const remove = document.createElement('button');

      link.href = `${action}?${new URLSearchParams({ on: item.on, pipeline: item.pipeline })}`;
      link.textContent = item.name;
      link.className = 'agg-saved-name';

      target.textContent = item.on;
      target.className = 'hint mono';

      remove.type = 'button';
      remove.className = 'btn small ghost';
      remove.textContent = 'Delete';
      remove.setAttribute('aria-label', `Delete the saved pipeline ${item.name}`);

      remove.addEventListener('click', () => {
        writeSaved(connection, readSaved(connection).filter(other => other.name !== item.name));
        paint();
      });

      row.className = 'agg-saved-row';
      row.append(link, target, remove);
      listBox!.append(row);
    }
  }

  save.addEventListener('click', () => {
    const name = nameInput.value.trim();

    if (name === '') {
      nameInput.focus();

      return;
    }

    const others = readSaved(connection).filter(item => item.name !== name);

    writeSaved(connection, [...others, { name, on: on.value.trim(), pipeline: raw.value }]);

    nameInput.value = '';
    paint();
  });

  opener.hidden = false;
  paint();
}

export function mount(): void {
  const form = document.querySelector<HTMLFormElement>('form[data-agg]');

  // A pipeline that the server could not parse stays in the plain textarea,
  // so the script does not replace the text that the user must correct.
  if (form && form.dataset.aggBroken === undefined) mountForm(form);
}
