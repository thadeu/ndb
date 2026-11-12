import type { FC } from 'hono/jsx';

import { copyText } from '@/components/copy';

import { Code } from './code';
import { Icon, iconSvg } from './icon';

// CodeBlock shows a command or a snippet with a Copy button. Without the
// script the text can still be selected.
export const CodeBlock: FC<{ code: string; label?: string }> = ({ code, label }) => (
  <div class="codebox">
    {label && <span class="codebox-label">{label}</span>}

    <pre class="codebox-pre">
      <Code text={code} />
    </pre>

    <button type="button" class="btn small codebox-copy" data-copy={code} aria-label="Copy" title="Copy">
      <Icon name="copy" size={16} />
    </button>
  </div>
);

export function mount(): void {
  document.addEventListener('click', async event => {
    const target = event.target;
    const button = target instanceof Element ? target.closest<HTMLElement>('[data-copy]') : null;

    if (!button) return;

    const copied = await copyText(button.dataset.copy ?? '');
    const before = button.innerHTML;
    const icon = button.querySelector('svg') !== null;

    if (icon) {
      button.innerHTML = copied ? iconSvg('check', 16) : before;
      button.title = copied ? 'Copied' : 'Press Ctrl+C';
    } else {
      button.textContent = copied ? 'Copied' : 'Press Ctrl+C';
    }

    setTimeout(() => {
      button.innerHTML = before;
      button.title = 'Copy';
    }, 1500);
  });
}
