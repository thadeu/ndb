import type { FC } from 'hono/jsx';

export interface ToggleItem {
  id: string;
  label: string;
}

export interface ToggleGroupProps {
  name: string;
  items: ToggleItem[];
  value: string;
  label: string;
  mono?: boolean;
}

// ToggleGroup is a large segmented control of a form: one value of a few,
// sent with the form. It is made of native radios, so it works without a
// script.
export const ToggleGroup: FC<ToggleGroupProps> = ({ name, items, value, label, mono = false }) => {
  const className = mono ? 'toggle-group mono' : 'toggle-group';

  return (
    <div class={className} role="radiogroup" aria-label={label}>
      {items.map(item => (
        <label class="toggle-item">
          <input
            type="radio"
            name={name}
            value={item.id}
            checked={item.id === value}
          />
          <span>{item.label}</span>
        </label>
      ))}
    </div>
  );
};
