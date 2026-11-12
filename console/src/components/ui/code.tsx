import { raw } from 'hono/html';
import type { FC } from 'hono/jsx';

import { highlightHtml } from './json-highlight';

// Code shows JSON (or a request: a method line and a JSON body) with colors.
// A script may change its text; the colors come back by themselves.
export const Code: FC<{ text: string; class?: string }> = ({ text, class: extra }) => (
  <code class={extra ? `hl ${extra}` : 'hl'} data-hl>
    {raw(highlightHtml(text))}
  </code>
);
