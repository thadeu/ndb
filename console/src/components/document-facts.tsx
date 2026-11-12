import type { FC } from 'hono/jsx';

import { Button } from '@/ui/button';

// DocFacts is the head of the side column of the document drawer: the _id
// with a copy button, the version, and the size. The server and the editor
// island draw the same markup. The copy button works through data-copy.
export const DocFacts: FC<{ id: string; version: string; bytes: number }> = ({ id, version, bytes }) => (
  <dl class="doc-facts">
    <div class="doc-fact doc-fact-id">
      <dt class="mono">_id</dt>

      <dd>
        <span class="doc-id mono">{id}</span>

        <Button size="sm" type="button" data-copy={id} aria-label="Copy the _id">
          Copy
        </Button>
      </dd>
    </div>

    <div class="doc-fact">
      <dt class="mono">_v</dt>
      <dd class="mono">{version}</dd>
    </div>

    <div class="doc-fact">
      <dt>Size</dt>
      <dd>{bytes.toLocaleString('en-US')} B</dd>
    </div>
  </dl>
);
