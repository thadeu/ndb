import type { Child, FC } from 'hono/jsx';

// Empty fills a page or a panel that has no data, and says what comes next.
export const Empty: FC<{ title: string; description?: string; action?: Child }> = ({ title, description, action }) => (
  <div class="empty-state">
    <p class="empty-title">{title}</p>
    {description && <p class="empty-desc">{description}</p>}
    {action}
  </div>
);
