import type { Child, FC } from 'hono/jsx';

export type BadgeTone = 'neutral' | 'pink' | 'ok' | 'warn' | 'bad';

// Badge is a short label. The lg size is the chip of a status row.
export const Badge: FC<{ tone?: BadgeTone; size?: 'md' | 'lg'; children?: Child }> = ({ tone = 'neutral', size = 'md', children }) => {
  const className = size === 'lg' ? `badge lg ${tone}` : `badge ${tone}`;

  return <span class={className}>{children}</span>;
};

export const Dot: FC<{ on: boolean }> = ({ on }) => <span class={on ? 'dot on' : 'dot'} aria-hidden="true" />;
