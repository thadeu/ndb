import type { Child, FC } from 'hono/jsx';

import { closePopover } from '@/lib/ui/popover';

import { Button, type ButtonProps } from './button';
import { Icon } from './icon';
import { Popover, popoverTrigger } from './popover';

export interface MenuProps {
  id: string;
  label: string;
  children?: Child;
}

// MenuButton is the button that opens the menu id.
export const MenuButton: FC<ButtonProps & { menu: string }> = ({ menu, children, ...rest }) => (
  <Button type="button" {...popoverTrigger(menu)} aria-haspopup="menu" {...rest}>
    {children}
    <Icon name="chevron-down" size={16} />
  </Button>
);

// Menu is a list of actions in a popover. Each item is a MenuItem: a link, a
// button that opens a dialog, or the submit button of a small form.
export const Menu: FC<MenuProps> = ({ id, label, children }) => (
  <Popover id={id} label={label} class="menu">
    {children}
  </Popover>
);

export interface MenuItemProps {
  tone?: 'default' | 'danger';
  href?: string;
  description?: string;
  children?: Child;
  [attribute: string]: unknown;
}

export const MenuItem: FC<MenuItemProps> = ({ tone = 'default', href, description, children, ...rest }) => {
  const className = tone === 'danger' ? 'menu-item danger' : 'menu-item';

  const content = (
    <>
      <span class="menu-item-label">{children}</span>
      {description && <span class="menu-item-desc">{description}</span>}
    </>
  );

  if (href) {
    return (
      <a class={className} href={href} {...rest}>
        {content}
      </a>
    );
  }

  return (
    <button class={className} {...rest}>
      {content}
    </button>
  );
};

// The menu closes when one of its items is used.
export function mount(): void {
  document.addEventListener('click', event => {
    const target = event.target;

    if (target instanceof Element && target.closest('.menu-item')) closePopover();
  });
}
