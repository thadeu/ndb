// Builds the curl line of a Dev Tools request. The token is never written:
// a TCP line holds the placeholder $NDB_TOKEN.

export interface CurlInput {
  method: string;
  path: string;
  body?: string;
  socket?: string;
  base?: string;
}

function quote(text: string): string {
  return `'${text.replace(/'/g, `'\\''`)}'`;
}

function compact(body: string): string {
  try {
    return JSON.stringify(JSON.parse(body));
  } catch {
    return body;
  }
}

export function buildCurl(input: CurlInput): string {
  const parts = ['curl'];

  if (input.socket) parts.push('--unix-socket', quote(input.socket));
  else parts.push('-H', '"Authorization: Bearer $NDB_TOKEN"');

  if (input.method !== 'GET') parts.push('-X', input.method);

  const origin = input.socket ? 'http://ndb' : (input.base ?? '').replace(/\/+$/, '');

  parts.push(quote(origin + input.path));

  if (input.body) parts.push('-d', quote(compact(input.body)));

  return parts.join(' ');
}
