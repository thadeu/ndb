import { getContext } from 'hono/context-storage';

import type { Connection } from './connections';
import { target } from './ndb';
import type { Env } from './prefix';

// databaseOf is the database a connection opens: the one picked in the
// switcher, else the one of its URL, else the default database.
export function databaseOf(connection: Connection): string {
  if (connection.database) return connection.database;

  try {
    return target(connection.url).database ?? 'default';
  } catch {
    return 'default';
  }
}

// openDatabases is the database list of the request (see listDatabases in
// app.tsx), or undefined when the daemon could not list them.
export function openDatabases(): string[] | undefined {
  try {
    return getContext<Env>().var.databases;
  } catch {
    return undefined;
  }
}
