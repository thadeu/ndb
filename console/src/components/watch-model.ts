// Pure logic of the Watch page: the settings in the URL, the body of the
// watch request, and the text of one event row. The route and the island
// use the same functions, so the page shows the request that the console
// sends.

export type WatchFrom = 'now' | 'beginning' | 'consumer';

export interface WatchSettings {
  coll: string;
  where: string;
  from: WatchFrom;
  consumer: string;
}

export const FROM_OPTIONS: { id: WatchFrom; label: string }[] = [
  { id: 'now', label: 'Now' },
  { id: 'beginning', label: 'The start' },
  { id: 'consumer', label: 'The position of a consumer' },
];

// A consumer name of the console: no spaces, so it is safe in a URL and in
// the log lines of the daemon.
export const CONSUMER = /^[A-Za-z0-9._:-]{1,128}$/;

export const PREVIEW_MAX = 160;

export function readFrom(value?: string | null): WatchFrom {
  return value === 'beginning' || value === 'consumer' ? value : 'now';
}

// settingsQuery is the query string of the settings. The defaults are left
// out, so a plain link stays short.
export function settingsQuery(settings: WatchSettings): string {
  const params = new URLSearchParams({ coll: settings.coll });

  if (settings.where.trim() !== '') params.set('where', settings.where);

  if (settings.from !== 'now') params.set('from', settings.from);

  if (settings.from === 'consumer' && settings.consumer !== '') params.set('consumer', settings.consumer);

  return params.toString();
}

export function sameSettings(a: WatchSettings, b: WatchSettings): boolean {
  return settingsQuery(a) === settingsQuery(b);
}

function hasKeys(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length > 0;
}

// checkSettings says why the console does not open this watch, or returns
// undefined. With a filter, ndb moves the saved position of the consumer
// past the events that it leaves out. The console must never move the
// position of a consumer of an app, so it refuses a filter with a consumer.
export function checkSettings(settings: WatchSettings, query: unknown): string | undefined {
  if (settings.from !== 'consumer') return undefined;

  if (!CONSUMER.test(settings.consumer)) return 'Write the name of the consumer: letters, digits, and . _ : - (at most 128).';

  if (hasKeys(query)) return 'Remove the filter. With a filter, ndb moves the saved position of this consumer, and the console must only read.';

  return undefined;
}

// watchBody is the body of POST /{coll}/_watch for the settings. The start
// of a named consumer is its saved position; a consumer with no position
// starts from now.
export function watchBody(settings: WatchSettings, query: unknown, consumer: string): Record<string, unknown> {
  const body: Record<string, unknown> = { consumer, from: settings.from === 'beginning' ? 'beginning' : 'now' };

  if (hasKeys(query)) body.query = query;

  return body;
}

function parseWhere(text: string): unknown {
  try {
    return text.trim() === '' ? undefined : JSON.parse(text);
  } catch {
    return undefined;
  }
}

// watchSnippet is the request of the settings, as an app sends it. The app
// picks its own consumer name: my-consumer stands for it while the field is
// empty.
export function watchSnippet(settings: WatchSettings): string {
  const consumer = settings.from === 'consumer' && settings.consumer !== '' ? settings.consumer : 'my-consumer';
  const body = watchBody(settings, parseWhere(settings.where), consumer);

  return `POST /${settings.coll}/_watch\n${JSON.stringify(body, null, 2)}`;
}

// preview is one line of a document, without _id and _v (the row shows
// them), cut at PREVIEW_MAX.
export function preview(doc?: Record<string, unknown>): string {
  if (!doc) return '';

  const { _id, _v, ...rest } = doc;
  const text = JSON.stringify(rest);

  return text.length > PREVIEW_MAX ? `${text.slice(0, PREVIEW_MAX - 1)}…` : text;
}

// agoText is the time since a heartbeat, such as "3 s ago".
export function agoText(at: number, now: number): string {
  const seconds = Math.max(0, Math.round((now - at) / 1000));

  if (seconds < 60) return `${seconds} s ago`;

  return `${Math.floor(seconds / 60)} min ago`;
}
