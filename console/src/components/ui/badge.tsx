import type { Child, FC } from 'hono/jsx';

export type BadgeTone = 'neutral' | 'pink' | 'ok';

export const Badge: FC<{ tone?: BadgeTone; children?: Child }> = ({ tone = 'neutral', children }) => <span class={`badge ${tone}`}>{children}</span>;

export const Dot: FC<{ on: boolean }> = ({ on }) => <span class={on ? 'dot on' : 'dot'} aria-hidden="true" />;
