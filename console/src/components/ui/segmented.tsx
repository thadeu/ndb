import type { FC } from 'hono/jsx';

export interface SegmentedItem {
  id: string;
  label: string;
}

export interface SegmentedProps {
  items: SegmentedItem[];
  value: string;
  label: string;
  onSelect?: (id: string) => void;
}

// Segmented switches between a few views of the same data, such as Tree and
// JSON. The island passes onSelect; the server renders it without handlers.
export const Segmented: FC<SegmentedProps> = ({ items, value, label, onSelect }) => (
  <div class="segmented" role="group" aria-label={label}>
    {items.map(item => (
      <button type="button" class={item.id === value ? 'seg-item on' : 'seg-item'} aria-pressed={item.id === value} onClick={onSelect ? () => onSelect(item.id) : undefined}>
        {item.label}
      </button>
    ))}
  </div>
);
