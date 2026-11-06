import type { FC } from 'hono/jsx';

export const Stat: FC<{ label: string; value: string; sub?: string }> = ({ label, value, sub }) => (
  <div class="card stat">
    <span class="stat-label">{label}</span>
    <span class="stat-value">{value}</span>
    {sub && <span class="stat-sub">{sub}</span>}
  </div>
);
