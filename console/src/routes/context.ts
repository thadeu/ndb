import type { Context } from 'hono';

import type { Connection, Connections } from '@/connections';
import { NAME } from '@/connections';
import type { Current } from '@/html/layout';
import type { Ndb, Stats } from '@/ndb';
import type { Env } from '@/prefix';

// RouteDeps is what the route modules of the tabs get from createApp.
export interface RouteDeps {
  connections: Connections;
  client: (connection: Connection) => Ndb;
  currentOf: (connection: Connection, stats?: Stats) => Current;
  message: (error: unknown) => string;
}

// connectionOf finds the connection of /c/:name, or undefined.
export function connectionOf(deps: RouteDeps, c: Context<Env>): Connection | undefined {
  const connection = deps.connections.get(c.req.param('name') ?? '');

  return connection && NAME.test(connection.name) ? connection : undefined;
}

// pageOrigin is the origin that the browser sees. Behind a proxy that ends
// TLS, the scheme comes from X-Forwarded-Proto.
export function pageOrigin(c: Context<Env>): string {
  const url = new URL(c.req.url);
  const proto = c.req.header('x-forwarded-proto') === 'https' ? 'https:' : url.protocol;

  return `${proto}//${url.host}`;
}

// sameOrigin is true when the Origin header is the console page itself. Every
// fetch POST of a browser sends Origin, so a missing one is refused.
export function sameOrigin(c: Context<Env>): boolean {
  return c.req.header('origin') === pageOrigin(c);
}

// redirectTo sends the browser to a path relative to the mount prefix.
export function redirectTo(c: Context<Env>, path: string) {
  return c.redirect(`${c.var.prefix}/${path}`, 303);
}

export function wantsJson(c: Context<Env>): boolean {
  return (c.req.header('content-type') ?? '').includes('json') || (c.req.header('accept') ?? '').includes('json');
}
