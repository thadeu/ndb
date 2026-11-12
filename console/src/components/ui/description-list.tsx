import type { Child, FC } from 'hono/jsx';

export interface DescriptionItem {
  label: string;
  value: Child;
}

// DescriptionList is a grid of label and value pairs, such as the general
// information of one collection. It has four columns on a wide screen and
// one on a phone.
export const DescriptionList: FC<{ items: DescriptionItem[] }> = ({ items }) => (
  <dl class="dlist">
    {items.map(item => (
      <div class="dlist-item">
        <dt class="dlist-label">{item.label}</dt>
        <dd class="dlist-value">{item.value}</dd>
      </div>
    ))}
  </dl>
);
