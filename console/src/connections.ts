import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

export interface Connection {
  name: string;
  url: string;
  token?: string;
}

export const NAME = /^[A-Za-z0-9._-]{1,64}$/;

const LOCAL: Connection = { name: 'local', url: 'unix:///run/ndb/ndb.sock' };

export function defaultPath(env: Record<string, string | undefined> = process.env): string {
  const base = env.XDG_CONFIG_HOME || join(homedir(), '.config');

  return join(base, 'ndb', 'console.json');
}

// fixedFromEnv gives the one connection of NDB_URL, or undefined.
export function fixedFromEnv(env: Record<string, string | undefined> = process.env): Connection | undefined {
  if (!env.NDB_URL) return undefined;

  return { name: env.NDB_NAME || 'default', url: env.NDB_URL, token: env.NDB_TOKEN || undefined };
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

  get(name: string): Connection | undefined {
    return this.list().find(connection => connection.name === name);
  }

  add(connection: Connection): void {
    if (this.fixed) throw new Error('The connection list is fixed by NDB_URL.');

    if (!NAME.test(connection.name)) throw new Error('Name: use letters, digits, dot, dash, or underscore.');

    const rest = this.list().filter(item => item.name !== connection.name);

    this.write([...rest, { name: connection.name, url: connection.url, ...(connection.token ? { token: connection.token } : {}) }]);
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

    list[index] = { name: connection.name, url: connection.url, ...(connection.token ? { token: connection.token } : {}) };

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
