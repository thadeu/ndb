import { getContext } from 'hono/context-storage';

import type { Connections } from './connections';

export interface Env {
  Variables: { prefix: string; connections: Connections };
}

const PREFIX = /^(\/[A-Za-z0-9._~-]+)*$/;

// readPrefix checks the path where a proxy mounts the console, such as /ndb.
// The proxy sends it in X-Forwarded-Prefix. Anything that is not a plain path
// becomes the root.
export function readPrefix(header: string | undefined): string {
  const value = (header ?? '').replace(/\/+$/, '');
  const dots = value.split('/').some(segment => segment === '.' || segment === '..');

  return PREFIX.test(value) && !dots ? value : '';
}

// prefix is the mount path of the current request. Pages use relative URLs
// with a <base> tag, so only the layout and the redirects need it.
export function prefix(): string {
  try {
    return getContext<Env>().var.prefix ?? '';
  } catch {
    return '';
  }
}
