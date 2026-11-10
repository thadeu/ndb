import { isIP } from 'node:net';

import type { MiddlewareHandler } from 'hono';

import { pageOrigin } from '@/routes/context';
import type { Env } from '@/prefix';

// The console has no login. These guards keep other sites out of it:
//
// - DNS rebinding: a page on evil.example can point its own name at
//   127.0.0.1 and then read the console as "same origin". The browser still
//   sends Host: evil.example, so a request whose Host is not an IP address,
//   localhost, or a name of the allowlist is refused.
// - CSRF: a write (POST, PUT, PATCH, DELETE) whose Origin is another site is
//   refused, whatever the content type.

const UNSAFE = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

// parseAllowedHosts reads NDB_UI_ALLOWED_HOSTS: names split by commas, such
// as "ndb.example.com,*.internal". "*.internal" allows every name under
// internal, not internal itself. Ports are not part of a name.
export function parseAllowedHosts(value: string | undefined): string[] {
  return (value ?? '')
    .split(',')
    .map(name => name.trim().toLowerCase().replace(/\.$/, ''))
    .filter(name => name !== '');
}

// hostAllowed is true when hostname (no port; IPv6 in brackets, as in a URL)
// is an IP address, localhost, or a name of allowed.
export function hostAllowed(hostname: string, allowed: string[]): boolean {
  const name = hostname.toLowerCase().replace(/\.$/, '');
  const bare = name.replace(/^\[(.*)\]$/, '$1');

  if (isIP(bare) !== 0 || name === 'localhost') return true;

  return allowed.some(entry => {
    if (entry.startsWith('*.')) return name.endsWith(entry.slice(1));

    return name === entry;
  });
}

// hostGuard refuses a request for a Host that is not allowed. /healthz stays
// open: a container check may call it by the service name, and it shows
// nothing.
export function hostGuard(allowed: string[]): MiddlewareHandler<Env> {
  return async (c, next) => {
    const hostname = new URL(c.req.url).hostname;

    if (c.req.path !== '/healthz' && !hostAllowed(hostname, allowed)) {
      return c.text('Forbidden: this host name is not allowed. Add it to NDB_UI_ALLOWED_HOSTS.', 403);
    }

    await next();
  };
}

// originGuard refuses a write from another site: an Origin that is not the
// console page, or a browser that says the request is cross-site. A request
// with no Origin (curl, a script) is left to the route checks.
export function originGuard(): MiddlewareHandler<Env> {
  return async (c, next) => {
    if (!UNSAFE.has(c.req.method)) return next();

    const origin = c.req.header('origin');
    const crossSite = c.req.header('sec-fetch-site') === 'cross-site';

    if ((origin !== undefined && origin !== pageOrigin(c)) || crossSite) {
      return c.text('Forbidden', 403);
    }

    await next();
  };
}
