import { raw } from 'hono/html';
import type { FC } from 'hono/jsx';

import mark from '@/assets/brand/mark-small-on-light.svg' with { type: 'text' };
import wordmark from '@/assets/brand/wordmark-on-light.svg' with { type: 'text' };

// The files in src/assets/brand are copies of /brand/svg in the repo, the
// ndb identity. Do not draw the mascot again: copy a new file from there.

// Logo is the ndb mascot, the ramen bowl. At this size the brand asks for
// the small drawing (no steam, two heavy noodles).
export const Logo: FC<{ size?: number }> = ({ size = 32 }) => (
  <span class="logo-mark" style={`width:${size}px;height:${size}px`}>
    {raw(mark)}
  </span>
);

// Wordmark is "ndb" in the brand letters. height is the height of the
// drawing in px; the width follows.
export const Wordmark: FC<{ height?: number }> = ({ height = 18 }) => (
  <span class="logo-wordmark" style={`height:${height}px`}>
    {raw(wordmark)}
  </span>
);
