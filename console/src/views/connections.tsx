import type { FC } from 'hono/jsx';

import { Layout } from '@/html/layout';
import type { Connection } from '@/connections';
import { formatUptime, formatVersion } from '@/format';
import type { Stats } from '@/ndb';
import { Badge, Dot } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { CodeBlock } from '@/ui/code-block';
import { openDialog } from '@/ui/dialog';
import { Drawer } from '@/ui/drawer';
import { Field } from '@/ui/field';
import { Icon } from '@/ui/icon';
import { Input } from '@/ui/input';

export interface ConnectionStatus {
  connection: Connection;
  online: boolean;
  stats?: Stats;
  streams?: number;
  error?: string;
}

export interface ConnectionsPageProps {
  items: ConnectionStatus[];
  fixed: boolean;
  notice?: string;
  error?: string;
  tested?: { ok: boolean; message: string };
  adding?: boolean;
  editing?: string;
}

type Tested = { ok: boolean; message: string };

const STACK_COMMAND = 'docker run -p 7070:7070 -p 4321:4321 \\\n  ghcr.io/thadeu/ndb-stack';

const editId = (name: string) => `edit-${name}`;

const plural = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`;

const isUnix = (connection: Connection) => connection.url.startsWith('unix://');

function transportText(connection: Connection): string {
  if (isUnix(connection)) return 'unix socket';

  return connection.token ? 'TCP · bearer token' : 'TCP · no token';
}

function tokenText(connection: Connection): string {
  if (connection.token) return 'token set';

  return isUnix(connection) ? 'no token needed' : 'no token';
}

function dataText(item: ConnectionStatus): string {
  if (!item.stats) return '— collections';

  const streams = item.streams ?? 0;
  const collections = plural(item.stats.totals.collections - streams, 'collection');

  return item.streams === undefined ? collections : `${collections} · ${plural(streams, 'stream')}`;
}

export const ConnectionsPage: FC<ConnectionsPageProps> = ({ items, fixed, notice, error, tested, adding = false, editing }) => (
  <Layout title="Connections">
    <div class="page-head">
      <div class="page-head-text">
        <span class="eyebrow">Connections</span>
        <h1>Pick an ndb to open.</h1>
        <p class="lead">A local socket, or a daemon on another VM over TCP with its token. Tokens stay on this server.</p>
      </div>

      {!fixed && (
        <Button
          variant="primary"
          class="conn-new"
          href="?new=1"
          {...openDialog('new-connection')}
        >
          <Icon name="plus" />
          New connection
        </Button>
      )}
    </div>

    {notice && <p class="note ok">{notice}</p>}
    {error && !adding && !editing && <p class="note bad">{error}</p>}

    {items.length > 0 && (
      <section class="grid-cards" aria-label="Saved connections">
        {items.map(item => (
          <ConnectionCard item={item} fixed={fixed} />
        ))}
      </section>
    )}

    <section class="grid-cards">
      {!fixed && <DiscoverCard />}

      <Card class="conn-side">
        <h2 class="card-title">Try ndb in one command</h2>

        <p class="conn-side-text">
          The <strong>ndb-stack</strong> image has the daemon and this console together, for development.
        </p>

        <CodeBlock code={STACK_COMMAND} />
      </Card>
    </section>

    {!fixed && (
      <ConnectionDrawer
        open={adding}
        tested={adding ? tested : undefined}
        error={adding ? error : undefined}
      />
    )}

    {!fixed &&
      items.map(({ connection }) => (
        <ConnectionDrawer
          connection={connection}
          open={editing === connection.name}
          tested={editing === connection.name ? tested : undefined}
          error={editing === connection.name ? error : undefined}
        />
      ))}
  </Layout>
);

// DiscoverCard finds a daemon from the page itself. A found daemon fills the
// field, and Save and open makes the connection.
const DiscoverCard: FC = () => (
  <Card class="conn-side">
    <h2 class="card-title">Discover</h2>

    <p class="conn-side-text">Type a host, or leave it empty for this machine. The console looks for a socket and for TCP on port 7070.</p>

    <form method="post" action="connections" class="discover" data-discover>
      <label class="label" for="discover-host">
        Host
      </label>

      <div class="discover-bar">
        <Input
          id="discover-host"
          name="url"
          mono
          placeholder="localhost"
          autocomplete="off"
          data-discover-host
        />

        <Button variant="dark" type="button" data-discover-run>
          Discover
        </Button>
      </div>

      <noscript>
        <p class="hint">Discover needs JavaScript. Use New connection and type the full URL.</p>
      </noscript>

      <p class="test-result" data-discover-status></p>

      <ul class="discover-list" data-discover-list></ul>

      <Button type="submit" class="discover-save">
        Save and open
      </Button>
    </form>
  </Card>
);

const ConnectionCard: FC<{ item: ConnectionStatus; fixed: boolean }> = ({ item, fixed }) => {
  const { connection, online, stats } = item;
  const href = `c/${encodeURIComponent(connection.name)}`;

  return (
    <article class="card conn">
      <div class="conn-title">
        <Dot on={online} />
        <a class="conn-name" href={href}>
          {connection.name}
        </a>
        <span class={online ? 'state on' : 'state'}>{online ? 'online' : 'offline'}</span>
      </div>

      <code class="conn-url">{connection.url}</code>

      <div class="conn-facts">
        <span>{dataText(item)}</span>
        <span>{stats ? `ndb ${formatVersion(stats.version)}` : '—'}</span>
        <span>{transportText(connection)}</span>
        <span>{stats ? `up ${formatUptime(stats.uptime_seconds)}` : '—'}</span>
      </div>

      {item.error && <span class="conn-error">{item.error}</span>}

      <div class="conn-foot">
        <Badge>{tokenText(connection)}</Badge>

        <span class="grow" />

        {!fixed && (
          <Button
            variant="ghost"
            href={`?edit=${encodeURIComponent(connection.name)}`}
            {...openDialog(editId(connection.name))}
          >
            Edit
          </Button>
        )}

        <Button href={href}>Open</Button>
      </div>
    </article>
  );
};

const resultClass = (tested?: Tested) => (tested ? (tested.ok ? 'test-result ok' : 'test-result bad') : 'test-result');

const TestRow: FC<{ tested?: Tested; submit: string }> = ({ tested, submit }) => (
  <div class="form-actions">
    <Button type="submit" formaction="connections/test" data-test>
      Test
    </Button>

    <span class={resultClass(tested)} data-test-result>
      {tested?.message}
    </span>

    <span class="grow" />

    <Button variant="primary" type="submit">
      {submit}
    </Button>
  </div>
);

interface ConnectionDrawerProps {
  connection?: Connection;
  tested?: Tested;
  error?: string;
  open: boolean;
}

// ConnectionDrawer is the one connection form, for New and for Edit. Edit
// fills it with the saved connection, but never with the token: an empty
// Token field keeps the saved one.
const ConnectionDrawer: FC<ConnectionDrawerProps> = ({ connection, tested, error, open }) => {
  const id = connection ? editId(connection.name) : 'new-connection';
  const action = connection ? `connections/${encodeURIComponent(connection.name)}/edit` : 'connections';
  const title = connection ? `Edit ${connection.name}` : 'New connection';

  const tokenHint = connection?.token ? 'empty keeps the saved token' : 'only for TCP with a token';

  return (
    <Drawer
      id={id}
      side="right"
      title={title}
      description="Discover an ndb on this machine or on a server, or type its URL."
      open={open}
      closeHref="./"
    >
      <form method="post" action={action} class="stack">
        {connection && <input type="hidden" name="current" value={connection.name} />}

        {error && <p class="note bad">{error}</p>}

        <Field id={`${id}-name`} label="Name">
          <Input
            id={`${id}-name`}
            name="name"
            value={connection?.name}
            placeholder="optional — taken from the URL"
            maxlength={64}
          />
        </Field>

        <Discover id={`${id}-url`} value={connection?.url} />

        <Field id={`${id}-token`} label="Token">
          <Input id={`${id}-token`} name="token" type="password" placeholder={tokenHint} autocomplete="off" />
        </Field>

        {connection?.token && (
          <label class="check">
            <input type="checkbox" name="clear_token" value="1" />
            <span>Remove the saved token</span>
          </label>
        )}

        <TestRow tested={tested} submit="Save and open" />
      </form>

      <p class="hint">
        Unix socket: <code class="mono">unix:///run/ndb/ndb.sock</code>. Over SSH: <code class="mono">ssh -L 7070:/run/ndb/ndb.sock vm</code>, then <code class="mono">tcp://127.0.0.1:7070</code>.
      </p>

      {connection && (
        <form method="post" action={`connections/${encodeURIComponent(connection.name)}/delete`} class="conn-remove">
          <span class="hint">Remove this connection from the console. The daemon and its data stay.</span>

          <Button variant="destructive" size="sm" type="submit">
            Remove
          </Button>
        </form>
      )}
    </Drawer>
  );
};

const Discover: FC<{ id: string; value?: string }> = ({ id, value }) => (
  <div class="discover" data-discover>
    <label class="label" for={id}>
      URL or host
    </label>

    <div class="discover-bar">
      <Input
        id={id}
        name="url"
        value={value}
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
