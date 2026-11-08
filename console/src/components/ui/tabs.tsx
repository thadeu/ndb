import type { Child, FC } from 'hono/jsx';

export interface TabItem {
  id: string;
  label: string;
  content: Child;
}

// Tabs switches between a few panels inside a card, such as the same command
// for systemd, Docker and Voodu. Radios and CSS do the switch, so it works
// without the script and keeps the choice in the form state. At most 8 tabs.
export const Tabs: FC<{ id: string; label: string; items: TabItem[]; value?: string }> = ({ id, label, items, value }) => {
  const selected = items.some(item => item.id === value) ? value : items[0]?.id;

  return (
    <div class="tabset" role="group" aria-label={label}>
      {items.map(item => (
        <input class="tabset-radio" type="radio" name={id} id={`${id}-${item.id}`} value={item.id} checked={item.id === selected} />
      ))}

      <div class="tabset-list">
        {items.map(item => (
          <label class="tabset-tab" for={`${id}-${item.id}`}>
            {item.label}
          </label>
        ))}
      </div>

      <div class="tabset-panels">
        {items.map(item => (
          <div class="tabset-panel">{item.content}</div>
        ))}
      </div>
    </div>
  );
};
