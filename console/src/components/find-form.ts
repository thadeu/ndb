import { buildWhere, readFind, searchBody } from '@/components/filter-model';

// The find bar of the Documents tab shows the _search request that a run
// sends, on one line. The server renders it for the last run; this script
// keeps it in step while the user types, before the run.
function refresh(form: HTMLFormElement): void {
  const preview = form.querySelector<HTMLElement>('[data-find-preview]');

  if (!preview) return;

  const params = new URLSearchParams();

  for (const [key, value] of new FormData(form)) params.append(key, String(value));

  const query = readFind(params);

  let text: string;

  if (query.mode === 'json') {
    try {
      const where = query.json.trim() === '' ? {} : JSON.parse(query.json);

      text = JSON.stringify(searchBody(query, where));
    } catch {
      text = 'The filter is not valid JSON.';
    }
  } else {
    const built = buildWhere(query.rows);

    text = built.error ?? JSON.stringify(searchBody(query, built.where));
  }

  const code = `${preview.dataset.findPreview} ${text}`;
  const pre = preview.querySelector('code');
  const copy = preview.querySelector<HTMLElement>('[data-copy]');

  if (pre) pre.textContent = code;

  if (copy) copy.dataset.copy = code;
}

export function mount(): void {
  for (const form of document.querySelectorAll<HTMLFormElement>('[data-find-form]')) {
    form.addEventListener('input', () => refresh(form));
    form.addEventListener('change', () => refresh(form));
  }

  document.addEventListener('change', event => {
    const control = event.target;

    if (control instanceof HTMLSelectElement && control.dataset.autosubmit !== undefined) control.form?.requestSubmit();
  });
}
