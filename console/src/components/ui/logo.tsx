import type { FC } from 'hono/jsx';

// Logo is the ndb bowl. The ink takes the text color.
export const Logo: FC<{ size?: number }> = ({ size = 30 }) => (
  <svg width={size} height={size} viewBox="0 0 32 32" fill="none" aria-hidden="true">
    <path d="M4 14h24a12 12 0 0 1-24 0Z" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round" />
    <path d="M9 14c1-4 3-4 4 0M15 14c1-5 3-5 4 0M21 14c1-4 2-4 3 0" stroke="#f0047f" stroke-width="2" stroke-linecap="round" />
    <circle cx="13" cy="20" r="1.2" fill="currentColor" />
    <circle cx="19" cy="20" r="1.2" fill="currentColor" />
  </svg>
);
