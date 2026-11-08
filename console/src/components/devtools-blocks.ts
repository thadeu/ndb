// Pure logic of the Dev Tools editor: split the text into request blocks,
// find the block under the cursor, format a body, and color a line. A block
// is a line `METHOD /path`, then an optional JSON body up to a blank line or
// the next method line.

export interface Block {
  method: string;
  path: string;
  body: string;
  start: number;
  end: number;
}

const METHOD_LINE = /^\s*(GET|POST|PUT|DELETE|PATCH|HEAD)\s+(\S+)\s*$/i;

// normalizePath adds the leading slash when the line omits it, as in Kibana:
// `POST orders/_search` sends `POST /orders/_search`.
export function normalizePath(path: string): string {
  return path.startsWith('/') ? path : `/${path}`;
}

export function isMethodLine(line: string): boolean {
  return METHOD_LINE.test(line);
}

export function parseBlocks(text: string): Block[] {
  const lines = text.split('\n');
  const blocks: Block[] = [];

  let current: Block | undefined;
  let body: string[] = [];

  const close = () => {
    if (!current) return;

    current.body = body.join('\n').trim();
    blocks.push(current);

    current = undefined;
    body = [];
  };

  lines.forEach((line, index) => {
    const match = METHOD_LINE.exec(line);

    if (match) {
      close();

      current = { method: (match[1] as string).toUpperCase(), path: normalizePath(match[2] as string), body: '', start: index, end: index };

      return;
    }

    if (!current) return;

    if (line.trim() === '') {
      close();

      return;
    }

    body.push(line);
    current.end = index;
  });

  close();

  return blocks;
}

// blockAt picks the block to run for a cursor line: the block that holds the
// line, else the next block below it, else the last block above it.
export function blockAt(blocks: Block[], line: number): Block | undefined {
  const inside = blocks.find(block => line >= block.start && line <= block.end);

  if (inside) return inside;

  const next = blocks.find(block => block.start > line);

  if (next) return next;

  return [...blocks].reverse().find(block => block.end < line);
}

// formatBlock pretty-prints the JSON body of a block in the text. It returns
// the new text, or undefined when there is no body or it is not JSON.
export function formatBlock(text: string, block: Block): string | undefined {
  if (block.end === block.start) return undefined;

  let pretty: string;

  try {
    pretty = JSON.stringify(JSON.parse(block.body), null, 2);
  } catch {
    return undefined;
  }

  const lines = text.split('\n');

  lines.splice(block.start + 1, block.end - block.start, ...pretty.split('\n'));

  return lines.join('\n');
}

// lineAt is the zero-based line of a character offset.
export function lineAt(text: string, offset: number): number {
  let line = 0;

  for (let index = 0; index < offset && index < text.length; index++) {
    if (text[index] === '\n') line++;
  }

  return line;
}

export interface Token {
  text: string;
  tone: 'method' | 'path' | 'key' | 'op' | 'string' | 'number' | 'dim' | 'plain';
}

const JSON_TOKEN = /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)|([^"\d\-tfn]+|[tfn\-\d])|[\s\S]/g;

// tokenize splits a line into colored parts for the overlay.
export function tokenize(line: string): Token[] {
  if (line.trimStart().startsWith('#')) return [{ text: line, tone: 'dim' }];

  const match = METHOD_LINE.exec(line);

  if (match) {
    const method = match[1] as string;
    const at = line.indexOf(method);
    const head = line.slice(0, at);
    const rest = line.slice(at + method.length);

    return [
      { text: head + method, tone: 'method' },
      { text: rest, tone: 'path' },
    ];
  }

  const tokens: Token[] = [];

  for (const part of line.matchAll(JSON_TOKEN)) {
    const [text, quoted, colon, literal] = part;

    if (quoted && colon) tokens.push({ text: quoted, tone: quoted.startsWith('"$') ? 'op' : 'key' }, { text: colon, tone: 'plain' });
    else if (quoted) tokens.push({ text: quoted, tone: 'string' });
    else if (literal) tokens.push({ text: literal, tone: 'number' });
    else tokens.push({ text, tone: 'plain' });
  }

  return tokens;
}

export function defaultText(collection: string): string {
  return [
    `# ${collection}: newest first`,
    `POST /${collection}/_search`,
    '{',
    '  "query": { "_id": { "$exists": true } },',
    '  "sort": { "_id": -1 },',
    '  "limit": 2,',
    '  "versions": true',
    '}',
    '',
    `POST /${collection}/_count`,
    '{ "query": { "_id": { "$exists": true } } }',
    '',
    `GET /${collection}/_stats`,
    '',
    `POST /${collection}/_update_by_query`,
    '{',
    '  "query": { "sku": "sku-1042" },',
    '  "update": {',
    '    "$inc": { "qty": -1 },',
    '    "$setOnInsert": { "status": "pending" }',
    '  },',
    '  "upsert": true',
    '}',
    '',
    `POST /${collection}/_aggregate`,
    '{ "pipeline": [',
    '  { "$group": { "_id": "$status", "n": { "$count": {} } } }',
    ']}',
    '',
    'GET /_stats',
    '',
  ].join('\n');
}
