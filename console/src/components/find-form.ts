import { buildWhere, needsValue, OPERATORS, readFind, searchBody } from '@/components/filter-model';
import { upgrade } from '@/ui/select';
import { allowedOps, NO_SEALING, type Sealing, sealHint } from '@/components/sealed-model';

function sealingOf(form: HTMLFormElement): Sealing {
  try {
    return JSON.parse(form.dataset.sealing ?? '') as Sealing;
  } catch {
    return NO_SEALING;
  }
}

// fitRow keeps the conditions of a row to the ones that its field allows,
// with the hint of an encrypted field.
function fitRow(row: HTMLElement, sealing: Sealing): void {
  const field = row.querySelector<HTMLInputElement>('input[name="f"]')?.value ?? '';
  const select = row.querySelector<HTMLSelectElement>('select[name="op"]');
  const hint = row.querySelector<HTMLElement>('[data-seal-hint]');

  if (!select) return;

  const allowed = allowedOps(field, sealing);
  const items = allowed ? OPERATORS.filter(item => allowed.includes(item.id)) : OPERATORS;
  const ids = items.map(item => item.id).join(',');

  if (select.dataset.ops !== ids) {
    const before = select.value;

    select.replaceChildren(...items.map(item => new Option(item.label, item.id)));
    select.value = items.some(item => item.id === before) ? before : (items[0]?.id ?? '');
    select.dataset.ops = ids;
    select.dispatchEvent(new Event('change', { bubbles: true }));

    const host = select.closest<HTMLElement>('[data-dd]');

    if (host) {
      host.querySelector('.dd-button')?.remove();
      host.querySelector('.dd-list')?.remove();
      upgrade(host);
    }
  }

  const value = row.querySelector<HTMLInputElement>('input[name="v"]');

  if (value) value.disabled = !needsValue(select.value);

  const text = sealHint(field, sealing);

  if (hint) {
    hint.textContent = text ?? '';
    hint.hidden = !text;
  }
}

// The find bar of the Documents tab shows the _search request that a run
// sends. The server renders it for the last run; this script keeps it in
// step while the user types, before the run.
function refresh(form: HTMLFormElement): void {
  const preview = form.querySelector<HTMLElement>('[data-find-preview]');

  if (!preview) return;

  const params = new URLSearchParams();

  for (const [key, value] of new FormData(form)) params.append(key, String(value));

  const query = readFind(params);

  let text: string;
  let body: unknown;

  if (query.mode === 'json') {
    try {
      const where = query.json.trim() === '' ? {} : JSON.parse(query.json);

      body = searchBody(query, where);
      text = JSON.stringify(body, null, 2);
    } catch {
      text = 'The filter is not valid JSON.';
    }
  } else {
    const built = buildWhere(query.rows);

    body = built.error ? undefined : searchBody(query, built.where);
    text = built.error ?? JSON.stringify(body, null, 2);
  }

  const code = `${preview.dataset.findPreview}\n${text}`;
  const pre = preview.querySelector('code');
  const copy = preview.querySelector<HTMLElement>('[data-copy]');

  if (pre) pre.textContent = code;

  if (copy) copy.dataset.copy = body === undefined ? code : `${preview.dataset.findPreview} ${JSON.stringify(body)}`;
}

export function mount(): void {
  for (const form of document.querySelectorAll<HTMLFormElement>('[data-find-form]')) {
    const sealing = sealingOf(form);

    form.addEventListener('input', event => {
      const row = event.target instanceof Element ? event.target.closest<HTMLElement>('.find-row') : null;

      if (row) fitRow(row, sealing);

      refresh(form);
    });

    form.addEventListener('change', event => {
      const row = event.target instanceof HTMLSelectElement ? event.target.closest<HTMLElement>('.find-row') : null;

      if (row) fitRow(row, sealing);

      refresh(form);
    });
  }

  // The request panel closes on a click outside it.
  document.addEventListener('click', event => {
    const inside = event.target instanceof Element ? event.target.closest('.find-request') : null;

    for (const panel of document.querySelectorAll<HTMLDetailsElement>('.find-request[open]')) {
      if (panel !== inside) panel.open = false;
    }
  });

  document.addEventListener('change', event => {
    const control = event.target;

    if (control instanceof HTMLSelectElement && control.dataset.autosubmit !== undefined) control.form?.requestSubmit();
  });
}
