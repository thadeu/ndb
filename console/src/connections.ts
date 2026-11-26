import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

export interface Connection {
  name: string;
  url: string;
  token?: string;

  // database is a database of the daemon other than the one of the URL,
  // picked in the switcher: the page path names it as <connection>@<db>.
  database?: string;
}

// DATABASE is the rule of a database name in the daemon.
export const DATABASE = /^[a-z0-9][a-z0-9_-]{0,62}$/;

// splitName reads <connection>@<database>, the name of a page of a saved
// connection in another database. A plain name has no database.
export function splitName(name: string): { base: string; database?: string } {
  const at = name.indexOf('@');

  if (at < 0) return { base: name };

  return { base: name.slice(0, at), database: name.slice(at + 1) };
}

export const NAME = /^[A-Za-z0-9._-]{1,64}$/;

const LOCAL: Connection = { name: 'local', url: 'unix:///run/ndb/ndb.sock' };

export function defaultPath(env: Record<string, string | undefined> = process.env): string {
  const base = env.XDG_CONFIG_HOME || join(homedir(), '.config');

  return join(base, 'ndb', 'console.json');
}

// fixedFromEnv gives the one connection of NDB_URL, or undefined. The token
// of the URL (tcp://:<token>@host:port) wins over NDB_TOKEN, and it is taken
// out of the URL, so the page never shows it.
export function fixedFromEnv(env: Record<string, string | undefined> = process.env): Connection | undefined {
  if (!env.NDB_URL) return undefined;

  const { url, token } = splitToken(env.NDB_URL);

  return { name: env.NDB_NAME || 'default', url, token: token || env.NDB_TOKEN || undefined };
}

// stored is the connection as the file keeps it: a token typed in the URL
// (tcp://:<token>@host) moves to the token field, so no page shows it.
function stored(connection: Connection): Connection {
  const { url, token } = splitToken(connection.url);
  const kept = token ?? connection.token;

  return { name: connection.name, url, ...(kept ? { token: kept } : {}) };
}

// splitToken takes the userinfo out of url: the password is the token, the
// user part is ignored.
export function splitToken(url: string): { url: string; token?: string } {
  const match = /^([a-z][a-z0-9+.-]*:\/\/)([^/@]*)@(.*)$/i.exec(url);

  if (!match) return { url };

  const [, scheme = '', userinfo = '', rest = ''] = match;
  const colon = userinfo.indexOf(':');
  const password = colon < 0 ? '' : decodeURIComponent(userinfo.slice(colon + 1));

  return { url: scheme + rest, token: password || undefined };
}

// Connections is the list of ndb daemons that the console can open. It lives
// in a JSON file with mode 0600, because it holds tokens. With a fixed
// connection, the file is not used and the list cannot change.
export class Connections {
  constructor(
    private readonly path: string,
    private readonly fixedConnection?: Connection,
  ) {}

  get fixed(): boolean {
    return this.fixedConnection !== undefined;
  }

  list(): Connection[] {
    if (this.fixedConnection) return [this.fixedConnection];

    if (!existsSync(this.path)) return [{ ...LOCAL }];

    try {
      const body = JSON.parse(readFileSync(this.path, 'utf8'));

      return Array.isArray(body.connections) ? body.connections : [];
    } catch {
      return [];
    }
  }

  // get finds a saved connection. <name>@<database> is that connection in
  // another database of its daemon; "default" is the default database.
  get(name: string): Connection | undefined {
    const { base, database } = splitName(name);
    const found = this.list().find(connection => connection.name === base);

    if (!found || database === undefined) return found;

    if (!DATABASE.test(database)) return undefined;

    return { ...found, name, database };
  }

  add(connection: Connection): void {
    if (this.fixed) throw new Error('The connection list is fixed by NDB_URL.');

    if (!NAME.test(connection.name)) throw new Error('Name: use letters, digits, dot, dash, or underscore.');

    const rest = this.list().filter(item => item.name !== connection.name);

    this.write([...rest, stored(connection)]);
  }

  // update replaces the connection `name` in place, so the list keeps its
  // order. The new name can differ, but not match another connection.
  update(name: string, connection: Connection): void {
    if (this.fixed) throw new Error('The connection list is fixed by NDB_URL.');

    if (!NAME.test(connection.name)) throw new Error('Name: use letters, digits, dot, dash, or underscore.');

    const list = this.list();
    const index = list.findIndex(item => item.name === name);

    if (index < 0) throw new Error(`No connection named ${name}.`);

    if (connection.name !== name && list.some(item => item.name === connection.name)) {
      throw new Error(`A connection named ${connection.name} already exists.`);
    }

    list[index] = stored(connection);

    this.write(list);
  }

  remove(name: string): void {
    if (this.fixed) throw new Error('The connection list is fixed by NDB_URL.');

    this.write(this.list().filter(item => item.name !== name));
  }

  private write(connections: Connection[]): void {
    mkdirSync(dirname(this.path), { recursive: true, mode: 0o700 });
    writeFileSync(this.path, JSON.stringify({ connections }, null, 2), { mode: 0o600 });
    chmodSync(this.path, 0o600);
  }
}
