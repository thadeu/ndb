import { buildWhere, readFind, searchBody } from '@/components/filter-model';

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
    form.addEventListener('input', () => refresh(form));
    form.addEventListener('change', () => refresh(form));
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
