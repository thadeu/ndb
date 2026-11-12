import type { FC } from 'hono/jsx';

export interface SegmentedNavItem {
  id: string;
  label: string;
  href: string;
}

// SegmentedNav has the look of Segmented, but each item is a link: the view
// lives in the URL, so it works without the script.
export const SegmentedNav: FC<{ items: SegmentedNavItem[]; value: string; label: string }> = ({ items, value, label }) => (
  <nav class="segmented" aria-label={label}>
    {items.map(item => (
      <a class={item.id === value ? 'seg-item on' : 'seg-item'} href={item.href} aria-current={item.id === value ? 'page' : undefined}>
        {item.label}
      </a>
    ))}
  </nav>
);
