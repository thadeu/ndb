import type { FC } from 'hono/jsx';

export interface NavTabItem {
  id: string;
  label: string;
  href: string;
}

// NavTabs is a row of links that pick one section of a card, such as the
// lists of a consumer group. The section lives in the URL. On a narrow
// screen the row scrolls sideways.
export const NavTabs: FC<{ items: NavTabItem[]; value: string; label: string }> = ({ items, value, label }) => (
  <nav class="navtabs" aria-label={label}>
    {items.map(item => (
      <a class={item.id === value ? 'navtab on' : 'navtab'} href={item.href} aria-current={item.id === value ? 'page' : undefined}>
        {item.label}
      </a>
    ))}
  </nav>
);
