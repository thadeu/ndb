import type { FC } from 'hono/jsx';

import { Layout } from '@/html/layout';
import type { Connection } from '@/connections';
import { formatBytes, formatCount, formatVersion } from '@/format';
import type { Stats } from '@/ndb';
import { Badge, Dot } from '@/ui/badge';
import { Button } from '@/ui/button';
import { openDialog } from '@/ui/dialog';
import { Drawer } from '@/ui/drawer';
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
  adding?: boolean;
}

export const ConnectionsPage: FC<ConnectionsPageProps> = ({ items, fixed, notice, error, tested, adding = false }) => (
  <Layout title="Connections">
    <div class="page-head">
      <div class="page-head-text">
        <span class="eyebrow">Connections</span>
        <h1>Pick an ndb to open.</h1>
        <p class="lead">A local socket, or a daemon on another VM over TCP with its token. Tokens stay on this server.</p>
      </div>
    </div>

    {notice && <p class="note ok">{notice}</p>}
    {error && <p class="note bad">{error}</p>}

    <div class="grid-cards">
      {items.map(item => (
        <ConnectionCard item={item} fixed={fixed} />
      ))}

      {!fixed && <AddCard />}
    </div>

    {!fixed && <NewConnection tested={tested} open={adding} />}
  </Layout>
);

const AddCard: FC = () => (
  <a class="card conn-add" href="?new=1" {...openDialog('new-connection')}>
    <span class="conn-add-icon" aria-hidden="true">+</span>
    <span class="conn-add-title">New connection</span>
    <span class="conn-add-hint">Discover a socket or a server, or type its URL.</span>
  </a>
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
          <span>{stats ? formatVersion(stats.version) : '—'}</span>
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

const NewConnection: FC<{ tested?: { ok: boolean; message: string }; open: boolean }> = ({ tested, open }) => {
  const resultClass = tested ? (tested.ok ? 'test-result ok' : 'test-result bad') : 'test-result';

  return (
    <Drawer
      id="new-connection"
      side="right"
      title="New connection"
      description="Discover an ndb on this machine or on a server, or type its URL."
      open={open}
      closeHref="./"
    >
      <form method="post" action="connections" class="stack">
        <Field id="conn-name" label="Name">
          <Input id="conn-name" name="name" placeholder="optional — taken from the URL" maxlength={64} />
        </Field>

        <Discover />

        <Field id="conn-token" label="Token">
          <Input id="conn-token" name="token" type="password" placeholder="only for TCP with a token" autocomplete="off" />
        </Field>

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
    </Drawer>
  );
};

const Discover: FC = () => (
  <div class="discover" data-discover>
    <label class="label" for="conn-url">
      URL or host
    </label>

    <div class="discover-bar">
      <Input
        id="conn-url"
        name="url"
        mono
        placeholder="unix:///run/ndb/ndb.sock, tcp://10.10.0.1:7070, or localhost"
        autocomplete="off"
        required
        data-discover-host
      />

      <Button type="button" data-discover-run>
        Discover
      </Button>
    </div>

    <p class="hint">Type the URL, or a host (empty is this machine) and press Discover to pick one.</p>

    <noscript>
      <p class="hint">Discover needs JavaScript. Type the full URL instead.</p>
    </noscript>

    <p class="test-result" data-discover-status></p>

    <ul class="discover-list" data-discover-list></ul>
  </div>
);
