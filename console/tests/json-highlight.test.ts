import { describe, expect, it } from 'bun:test';

import { highlightHtml } from '@/ui/json-highlight';

describe('JSON highlight', () => {
  it('colors keys, operators, strings, numbers, booleans and null', () => {
    const html = highlightHtml('{"a": "x", "$gte": 1, "ok": true, "n": null}');

    expect(html).toContain('<span class="hl-key">"a"</span>');
    expect(html).toContain('<span class="hl-op">"$gte"</span>');
    expect(html).toContain('<span class="hl-str">"x"</span>');
    expect(html).toContain('<span class="hl-num">1</span>');
    expect(html).toContain('<span class="hl-bool">true</span>');
    expect(html).toContain('<span class="hl-null">null</span>');
  });

  it('colors a request line and keeps the text the same', () => {
    const text = 'POST /orders/_search\n{"query": {"a": "<b>"}}';
    const html = highlightHtml(text);

    expect(html).toContain('<span class="hl-method">POST</span>');
    expect(html).not.toContain('<b>');
    expect(html.replace(/<[^>]+>/g, '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&')).toBe(text);
  });
});
