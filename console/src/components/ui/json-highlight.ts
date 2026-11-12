import { tokenize } from '@/components/devtools-blocks';

// JSON highlight for every place that shows JSON. The server calls
// highlightHtml so the colors are there at the first paint. A script that
// changes the text of an element with data-hl gets the colors again from
// mount(), which watches those elements.

const TONES: Record<string, string> = { key: 'hl-key', op: 'hl-op', string: 'hl-str', method: 'hl-method', path: 'hl-path', dim: 'hl-dim' };

function escape(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function toneOf(tone: string, text: string): string | undefined {
  if (tone !== 'number') return TONES[tone];

  if (text === 'true' || text === 'false') return 'hl-bool';

  if (text === 'null') return 'hl-null';

  return 'hl-num';
}

// highlightHtml gives the text as HTML with a span for each colored part. A
// line with a method and a path (POST /orders/_search) gets its own colors.
export function highlightHtml(text: string): string {
  return text
    .split('\n')
    .map(line =>
      tokenize(line)
        .map(token => {
          const tone = toneOf(token.tone, token.text.trim());
          const html = escape(token.text);

          return tone ? `<span class="${tone}">${html}</span>` : html;
        })
        .join(''),
    )
    .join('\n');
}

function paint(element: HTMLElement): void {
  const text = element.textContent ?? '';

  if (element.dataset.hlText === text) return;

  element.dataset.hlText = text;
  element.innerHTML = highlightHtml(text);
}

export function mount(): void {
  for (const element of document.querySelectorAll<HTMLElement>('[data-hl]')) element.dataset.hlText = element.textContent ?? '';

  const observer = new MutationObserver(records => {
    const changed = new Set<HTMLElement>();

    for (const record of records) {
      const node = record.target instanceof HTMLElement ? record.target : record.target.parentElement;
      const element = node?.closest<HTMLElement>('[data-hl]');

      if (element) changed.add(element);
    }

    for (const element of changed) paint(element);
  });

  observer.observe(document.body, { subtree: true, childList: true, characterData: true });
}
