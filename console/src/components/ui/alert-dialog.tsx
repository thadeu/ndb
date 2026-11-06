import type { FC } from 'hono/jsx';

export interface ConfirmOptions {
  title: string;
  body: string;
  action: string;
  tone?: 'danger' | 'default';
}

// confirm returns the attributes of a form that must be confirmed before it
// submits. Without the script, the form submits at once. The page needs one
// ConfirmHost.
export const confirm = (options: ConfirmOptions) => ({ 'data-confirm': JSON.stringify(options) });

export const ConfirmHost: FC = () => (
  <dialog id="confirm" class="dialog confirm" role="alertdialog" aria-labelledby="confirm-title" aria-describedby="confirm-body">
    <form method="dialog">
      <div class="dialog-head">
        <h2 class="dialog-title" id="confirm-title">
          Confirm
        </h2>

        <p class="dialog-desc" id="confirm-body" />
      </div>

      <div class="dialog-foot">
        <button class="btn" value="cancel" autofocus>
          Cancel
        </button>

        <button class="btn destructive" value="confirm" data-confirm-action>
          Confirm
        </button>
      </div>
    </form>
  </dialog>
);

export function mount(): void {
  const dialog = document.getElementById('confirm');

  if (!(dialog instanceof HTMLDialogElement)) return;

  const title = dialog.querySelector<HTMLElement>('#confirm-title');
  const body = dialog.querySelector<HTMLElement>('#confirm-body');
  const action = dialog.querySelector<HTMLButtonElement>('[data-confirm-action]');

  if (!title || !body || !action) return;

  let pending: { form: HTMLFormElement; submitter: HTMLElement | null } | null = null;

  document.addEventListener(
    'submit',
    event => {
      const form = event.target;

      if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || form.dataset.confirmed === '1') return;

      event.preventDefault();

      const options = JSON.parse(form.dataset.confirm) as ConfirmOptions;

      pending = { form, submitter: (event as SubmitEvent).submitter ?? null };

      title.textContent = options.title;
      body.textContent = options.body;
      action.textContent = options.action;
      action.className = options.tone === 'default' ? 'btn primary' : 'btn destructive';

      dialog.returnValue = '';
      dialog.showModal();
    },
    true,
  );

  dialog.addEventListener('close', () => {
    const current = pending;

    pending = null;

    if (!current || dialog.returnValue !== 'confirm') return;

    current.form.dataset.confirmed = '1';
    current.form.requestSubmit(current.submitter instanceof HTMLButtonElement ? current.submitter : undefined);

    delete current.form.dataset.confirmed;
  });
}
