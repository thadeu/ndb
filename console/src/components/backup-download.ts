// mount upgrades the backup form: it fetches the file and saves it through a
// temporary <a download> link, so the page stays where it is. Without the
// script, the form posts and the browser handles the attachment.
export function mount(): void {
  const form = document.querySelector<HTMLFormElement>('[data-backup-form]');
  const status = document.querySelector<HTMLElement>('[data-backup-status]');
  const button = form?.querySelector<HTMLButtonElement>('button[type="submit"]');

  if (!form || !status || !button) return;

  const say = (text: string, tone: 'ok' | 'bad' | '') => {
    status.textContent = text;
    status.className = tone ? `hint ${tone}` : 'hint';
    status.hidden = text === '';
  };

  form.addEventListener('submit', async event => {
    event.preventDefault();

    button.disabled = true;
    say('Taking the backup…', '');

    try {
      const response = await fetch(form.action, { method: 'POST', body: new FormData(form) });
      const disposition = response.headers.get('content-disposition') ?? '';
      const name = /filename="([^"]+)"/.exec(disposition)?.[1];

      if (!response.ok || !name) {
        const error = new URL(response.url).searchParams.get('error');

        say(error ?? 'The backup failed.', 'bad');

        return;
      }

      save(await response.blob(), name);
      say(`Saved ${name}.`, 'ok');
    } catch {
      say('The backup failed.', 'bad');
    } finally {
      button.disabled = false;
    }
  });
}

function save(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');

  link.href = url;
  link.download = name;
  link.hidden = true;

  document.body.append(link);
  link.click();
  link.remove();

  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
