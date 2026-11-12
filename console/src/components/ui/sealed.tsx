import type { FC } from 'hono/jsx';

// Sealed is the small gray pill that takes the place of an encrypted value.
// It draws its lock with plain SVG nodes, so the islands can use it too.
export const Sealed: FC<{ title?: string }> = ({ title = 'Encrypted value' }) => (
  <span class="sealed" title={title}>
    <svg
      class="sealed-lock"
      viewBox="0 0 24 24"
      aria-hidden="true"
    >
      <rect x="5" y="11" width="14" height="10" rx="2" />
      <path d="M8 11V8a4 4 0 018 0v3" />
    </svg>

    encrypted
  </span>
);
