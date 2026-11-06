import type { FC } from 'hono/jsx';

import { Layout } from '@/html/layout';
import type { Connection } from '@/connections';
import { formatBytes, formatCount } from '@/format';
import type { Stats } from '@/ndb';
import { Badge, Dot } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';

export interface ConnectionStatus {
  connection: Connection;
  online: boolean;
  stats?: Stats;
  error?: string;
}

export interface ConnectionsPageProps {
  items: ConnectionStatus[];
  fixed: boolean;
  notice?: string;
  error?: string;
  tested?: { ok: boolean; message: string };
}

export const ConnectionsPage: FC<ConnectionsPageProps> = ({ items, fixed, notice, error, tested }) => (
  <Layout title="Connections">
    <div class="page-head">
      <div class="page-head-text">
        <span class="eyebrow">Connections</span>
        <h1>Pick an ndb to open.</h1>
        <p class="lead">A local socket, or a daemon on another VM over TCP with its token. Tokens stay on this server.</p>
      </div>

      {!fixed && (
        <Button variant="dark" href="#new">
          New connection
        </Button>
      )}
    </div>

    {notice && <p class="note ok">{notice}</p>}
    {error && <p class="note bad">{error}</p>}

    <div class="grid-cards">
      {items.map(item => (
        <ConnectionCard item={item} fixed={fixed} />
      ))}
    </div>

    {!fixed && <NewConnection tested={tested} />}
  </Layout>
);

const ConnectionCard: FC<{ item: ConnectionStatus; fixed: boolean }> = ({ item, fixed }) => {
  const { connection, online, stats } = item;
  const href = `c/${encodeURIComponent(connection.name)}`;

  return (
    <div class="card conn">
      <a class="conn-main" href={href}>
        <div class="conn-title">
          <Dot on={online} />
          <span class="conn-name">{connection.name}</span>
          <span class="grow" />
          <span class={online ? 'state on' : 'state'}>{online ? 'online' : 'offline'}</span>
        </div>

        <code class="url">{connection.url}</code>

        <div class="conn-facts">
          <span>{stats ? `${stats.totals.collections} collections` : '— collections'}</span>
          <span>{stats ? `${formatCount(stats.totals.rows)} rows` : '— rows'}</span>
          <span>{stats ? formatBytes(stats.totals.bytes) : '—'}</span>
          <span>{stats ? `v${stats.version}` : '—'}</span>
        </div>

        {item.error && <span class="conn-error">{item.error}</span>}
      </a>

      <div class="conn-foot">
        <Badge>{connection.token ? 'token set' : 'no token'}</Badge>

        {!fixed && (
          <form method="post" action={`connections/${encodeURIComponent(connection.name)}/delete`}>
            <Button variant="ghost" size="sm" type="submit">
              Remove
            </Button>
          </form>
        )}
      </div>
    </div>
  );
};

const NewConnection: FC<{ tested?: { ok: boolean; message: string } }> = ({ tested }) => {
  const resultClass = tested ? (tested.ok ? 'test-result ok' : 'test-result bad') : 'test-result';

  return (
    <Card class="form-card">
      <h2 id="new" class="card-title">
        New connection
      </h2>

      <form method="post" action="connections" class="stack">
        <div class="grid-fields">
          <Field id="conn-name" label="Name">
            <Input id="conn-name" name="name" placeholder="prod-db" required maxlength={64} />
          </Field>

          <Field id="conn-url" label="URL">
            <Input id="conn-url" name="url" mono placeholder="tcp://10.10.0.1:7070" required />
          </Field>

          <Field id="conn-token" label="Token">
            <Input id="conn-token" name="token" type="password" autocomplete="off" />
          </Field>
        </div>

        <div class="form-actions">
          <Button type="submit" formaction="connections/test" data-test>
            Test
          </Button>

          <span class={resultClass} data-test-result>
            {tested?.message}
          </span>

          <span class="grow" />

          <Button variant="primary" type="submit">
            Save and open
          </Button>
        </div>
      </form>

      <p class="hint">
        Unix socket: <code class="mono">unix:///run/ndb/ndb.sock</code>. Over SSH: <code class="mono">ssh -L 7070:/run/ndb/ndb.sock vm</code>, then <code class="mono">tcp://127.0.0.1:7070</code>.
      </p>
    </Card>
  );
};
