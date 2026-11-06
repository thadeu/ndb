import { useSend } from '@/hooks/useSend';

// mount upgrades the Insert form: it posts in place, shows an error inside
// the dialog, and opens the new document. Without the script, the form posts
// and the page redirects.
export function mount(): void {
  const form = document.querySelector<HTMLFormElement>('[data-insert-form]');
  const error = document.querySelector<HTMLElement>('[data-insert-error]');

  if (!form || !error) return;

  const send = useSend(form.getAttribute('action') ?? 'insert');

  form.addEventListener('submit', async event => {
    event.preventDefault();

    const data = new FormData(form);
    const answer = await send<{ message?: string; location?: string }>({ coll: data.get('coll'), doc: data.get('doc') });

    if (answer.ok && answer.body.location) {
      window.location.assign(answer.body.location);

      return;
    }

    error.textContent = answer.body.message ?? 'The insert failed.';
    error.hidden = false;
  });
}
