import type { FC } from 'hono/jsx';

import { copyText } from '@/components/copy';

// CodeBlock shows a command or a snippet with a Copy button. Without the
// script the text can still be selected.
export const CodeBlock: FC<{ code: string; label?: string }> = ({ code, label }) => (
  <div class="codebox">
    {label && <span class="codebox-label">{label}</span>}

    <pre class="codebox-pre">
      <code>{code}</code>
    </pre>

    <button type="button" class="btn small codebox-copy" data-copy={code}>
      Copy
    </button>
  </div>
);

export function mount(): void {
  document.addEventListener('click', async event => {
    const target = event.target;
    const button = target instanceof Element ? target.closest<HTMLElement>('[data-copy]') : null;

    if (!button) return;

    const copied = await copyText(button.dataset.copy ?? '');
    const label = button.textContent;

    button.textContent = copied ? 'Copied' : 'Press Ctrl+C';

    setTimeout(() => {
      button.textContent = label;
    }, 1500);
  });
}
