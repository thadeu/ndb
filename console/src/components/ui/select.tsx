import type { FC } from 'hono/jsx';

import { iconSvg } from './icon';

export interface SelectOption {
  id: string;
  label: string;
}

export interface SelectProps {
  options: SelectOption[];
  value?: string;
  class?: string;
  [attribute: string]: unknown;
}

// Select is a dropdown with the look of Input. The server renders a native
// select, so a form works without the script. The script puts a button and a
// list on top of it, and keeps the native select as the form value.
export const Select: FC<SelectProps> = ({ options, value, class: extra, ...rest }) => {
  const className = ['input', 'select', extra ?? ''].filter(Boolean).join(' ');

  return (
    <div class="dd" data-dd>
      <select class={className} {...rest}>
        {options.map(option => (
          <option value={option.id} selected={option.id === value}>
            {option.label}
          </option>
        ))}
      </select>
    </div>
  );
};

function close(host: HTMLElement): void {
  host.classList.remove('open');
  host.querySelector('.dd-button')?.setAttribute('aria-expanded', 'false');
}

function closeAll(except?: Element | null): void {
  for (const host of document.querySelectorAll<HTMLElement>('.dd.open')) {
    if (host !== except) close(host);
  }
}

// choose sets the native select and tells the form, so a script that listens
// for change (such as the request preview) sees the new value.
function choose(host: HTMLElement, select: HTMLSelectElement, index: number): void {
  const count = select.options.length;

  select.selectedIndex = Math.max(0, Math.min(count - 1, index));
  select.dispatchEvent(new Event('change', { bubbles: true }));

  paint(host, select);
}

function paint(host: HTMLElement, select: HTMLSelectElement): void {
  const text = host.querySelector('.dd-text');

  if (text) text.textContent = select.selectedOptions[0]?.textContent ?? '';

  host.querySelectorAll('.dd-option').forEach((item, index) => {
    item.classList.toggle('on', index === select.selectedIndex);
    item.setAttribute('aria-selected', String(index === select.selectedIndex));
  });
}

function upgrade(host: HTMLElement): void {
  const select = host.querySelector('select');

  if (!select || host.querySelector('.dd-button')) return;

  const button = document.createElement('button');

  button.type = 'button';
  button.className = 'input dd-button';
  button.setAttribute('aria-haspopup', 'listbox');
  button.setAttribute('aria-expanded', 'false');
  button.setAttribute('aria-label', select.getAttribute('aria-label') ?? select.name);
  button.innerHTML = `<span class="dd-text"></span><span class="dd-chev">${iconSvg('chevron-down')}</span>`;

  const list = document.createElement('ul');

  list.className = 'dd-list';
  list.setAttribute('role', 'listbox');

  for (const option of select.options) {
    const item = document.createElement('li');

    item.className = 'dd-option';
    item.setAttribute('role', 'option');
    item.textContent = option.textContent;

    list.append(item);
  }

  select.classList.add('dd-native');
  select.tabIndex = -1;
  host.append(button, list);

  paint(host, select);

  button.addEventListener('click', () => {
    const open = !host.classList.contains('open');

    closeAll(host);
    host.classList.toggle('open', open);
    button.setAttribute('aria-expanded', String(open));
  });

  button.addEventListener('keydown', event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      choose(host, select, select.selectedIndex + (event.key === 'ArrowDown' ? 1 : -1));
    }

    if (event.key === 'Escape') close(host);
  });

  list.addEventListener('click', event => {
    const item = event.target instanceof Element ? event.target.closest('.dd-option') : null;

    if (!item) return;

    choose(host, select, [...list.children].indexOf(item));
    close(host);
    button.focus();
  });

  select.addEventListener('change', () => paint(host, select));
}

export function mount(): void {
  for (const host of document.querySelectorAll<HTMLElement>('[data-dd]')) upgrade(host);

  document.addEventListener('click', event => {
    const host = event.target instanceof Element ? event.target.closest('.dd') : null;

    closeAll(host);
  });
}
