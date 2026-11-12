import type { FC } from 'hono/jsx';

import { Layout, type Current } from '@/html/layout';
import { formatBytes, formatCount } from '@/format';
import { type ClusterSettings, type EncryptionKey, keyInDataDir, LOG_LEVELS, type LogSettings, type Metrics, type Setting } from '@/operations';
import { Badge } from '@/ui/badge';
import { Button } from '@/ui/button';
import { Card } from '@/ui/card';
import { Icon } from '@/ui/icon';
import { Input } from '@/ui/input';
import { ToggleGroup } from '@/ui/toggle-group';

export interface OperationsPageProps {
  current: Current;
  unix: boolean;
  hasToken: boolean;
  log?: LogSettings;
  logError?: string;
  logForbidden?: boolean;
  metrics?: Metrics;
  settings?: SettingsState;
  notice?: string;
  warnings?: string[];
  error?: string;
}

// SettingsState is the answer of GET /_cluster/settings, or why the page
// does not have it.
export interface SettingsState {
  settings?: ClusterSettings;
  older?: boolean;
  forbidden?: boolean;
  error?: string;
}

export const KEY_DOCS = 'https://ndb.clowk.in/security#the-encryption-key';

interface Check {
  label: string;
  value: string;
  ok: boolean;
}

const LEVEL_ITEMS = LOG_LEVELS.map(level => ({ id: level, label: level }));

// securityChecks lists only what the console can see: the transport and the
// token of this connection, and the log settings. The daemon has no route
// for its other security settings.
function securityChecks({ current, unix, hasToken, log, logForbidden }: OperationsPageProps): Check[] {
  const address = current.url.replace(/^[a-z]+:\/\//, '');
  const checks: Check[] = [];

  if (unix) {
    checks.push({ label: 'Unix socket', value: address, ok: true });
  } else {
    checks.push({ label: 'TCP listener', value: `${address}, ${hasToken ? 'token' : 'no token'}`, ok: hasToken });
  }

  if (log) checks.push({ label: 'Admin routes (/_log)', value: 'allowed', ok: true });

  if (logForbidden) checks.push({ label: 'Admin routes (/_log)', value: 'refused for this token', ok: false });

  if (log) {
    const bodies = log.level === 'debug' && log.body_max > 0;

    checks.push({ label: 'Request bodies in the logs', value: bodies ? `on, up to ${log.body_max} bytes` : 'off', ok: !bodies });
  }

  return checks;
}

function tokenBadge(unix: boolean, hasToken: boolean): string {
  if (hasToken) return 'token set';

  return unix ? 'no token needed' : 'not set';
}

function scopeText(props: OperationsPageProps): string | undefined {
  if (props.unix) return 'full access, the socket file is the access control';

  if (props.log) return 'admin of *';

  if (props.logForbidden) return 'scoped, no admin of *';

  return undefined;
}

export const OperationsPage: FC<OperationsPageProps> = props => {
  const { current, unix, hasToken, log, logError, metrics, settings = {}, notice, warnings = [], error } = props;

  const checks = securityChecks(props);
  const open = checks.filter(check => !check.ok).length;
  const scope = scopeText(props);

  return (
    <Layout title="Operations" current={current} tab="operations">
      <header class="dash-head">
        <div class="dash-head-text">
          <h1 class="dash-title">Operations</h1>
          <p class="dash-lead">Logs, security and limits of this daemon. Apply stores the log settings in the data dir, so they hold across a restart.</p>
        </div>
      </header>

      {notice && <p class="note ok">{notice}</p>}
      {warnings.map(warning => (
        <p class="note warn">{warning}</p>
      ))}

      {error && <p class="note bad">{error}</p>}

      {keyInDataDir(settings.settings?.encryption?.source) && <KeyWarning />}

      <section class="card flush">
        {log ? (
          <LogForm log={log} action={`c/${encodeURIComponent(current.name)}/operations/log`} />
        ) : (
          <div class="ops-log">
            <h2 class="card-title">Logs</h2>
            <span class="hint">{logError ?? 'The daemon did not give its log settings.'}</span>
          </div>
        )}
      </section>

      <SettingsCard state={settings} />

      <div class="dash-grid">
        <Card>
          <div class="dash-card-head">
            <h2 class="card-title">Security</h2>
            <Badge tone={open > 0 ? 'warn' : 'ok'}>{open === 0 ? 'nothing to check' : open === 1 ? '1 thing to check' : `${open} things to check`}</Badge>
          </div>

          <ul class="ops-checks">
            {checks.map(check => (
              <li class="ops-check">
                <span class={check.ok ? 'ops-dot ok' : 'ops-dot warn'} aria-hidden="true" />
                <span class="ops-check-label">{check.label}</span>
                <span class="ops-check-value mono">{check.value}</span>
              </li>
            ))}
          </ul>

          <p class="ops-note">
            <strong>A token is the best protection.</strong> With a token on TCP, a web page cannot read or write anything.
          </p>
        </Card>

        <div class="ops-column">
          <Card>
            <h2 class="card-title">Tokens</h2>

            <ul class="ops-rows">
              <li class="ops-row">
                <span class="ops-row-label">token of this connection</span>
                <Badge>{tokenBadge(unix, hasToken)}</Badge>
              </li>

              {scope && (
                <li class="ops-row">
                  <span class="ops-row-label">scope</span>
                  <span class="ops-check-value mono">{scope}</span>
                </li>
              )}
            </ul>

            <span class="hint">The console shows only that a token is set, never its value.</span>
          </Card>

          {metrics && <UseCard metrics={metrics} />}
        </div>
      </div>
    </Layout>
  );
};

const LogForm: FC<{ log: LogSettings; action: string }> = ({ log, action }) => (
  <form method="post" action={action}>
    <div class="ops-log">
      <h2 class="card-title">Logs</h2>

      <ToggleGroup
        name="level"
        label="Log level"
        mono
        value={log.level}
        items={LEVEL_ITEMS}
      />

      <label class="ops-field">
        body_max
        <Input
          name="body_max"
          mono
          inputmode="numeric"
          pattern="[0-9]*"
          value={String(log.body_max)}
        />
      </label>

      <label class="ops-field">
        slow_ms
        <Input
          name="slow_ms"
          mono
          inputmode="numeric"
          pattern="[0-9]*"
          value={String(log.slow_ms)}
        />
      </label>

      <span class="grow" />

      <Button variant="dark" type="submit">
        Apply
      </Button>
    </div>

    <div class="ops-log-foot">
      <span>
        format <span class="mono">{log.format}</span>
      </span>
      <span>body_max is the largest body in a log line, in bytes. A request over slow_ms is a warn line. 0 turns each off.</span>
    </div>
  </form>
);

// KeyWarning shows when the daemon keeps its encryption key in the data
// dir: a copy of the disk or of a backup has the key with the data.
const KeyWarning: FC = () => (
  <section
    class="card ops-key-warn"
    role="note"
    data-key-warning
  >
    <span class="ops-key-icon">
      <Icon name="key" size={20} />
    </span>

    <div class="ops-key-text">
      <h2 class="card-title">The encryption key is in the data dir</h2>

      <p>
        A copy of the disk or of a backup has the key with the data, so it can decrypt the encrypted fields. Set <code class="mono">NDB_ENCRYPTION_KEY</code> (64 hex characters) from a secret store, and keep a copy of the key.
      </p>

      <a
        class="dash-link"
        href={KEY_DOCS}
        target="_blank"
        rel="noopener noreferrer"
      >
        Read about the encryption key
      </a>
    </div>
  </section>
);

const SOURCE_TONE: Record<string, 'neutral' | 'pink' | 'ok' | 'warn'> = {
  default: 'neutral',
  stored: 'ok',
  made: 'warn',
  env: 'ok',
  yml: 'pink',
  flag: 'pink',
};

const SourceBadge: FC<{ source: string }> = ({ source }) => <Badge tone={SOURCE_TONE[source] ?? 'neutral'}>{source}</Badge>;

const SettingRow: FC<{ name: string; setting?: Setting<string | number> }> = ({ name, setting }) => {
  if (!setting) return null;

  return (
    <li class="ops-row ops-setting">
      <span class="ops-row-label mono">{name}</span>
      <span class="ops-check-value mono">{String(setting.value)}</span>
      <SourceBadge source={setting.source} />
    </li>
  );
};

const KeyRow: FC<{ name: string; value?: string }> = ({ name, value }) => {
  if (!value) return null;

  return (
    <li class="ops-row ops-setting">
      <span class="ops-row-label">{name}</span>
      <span class="ops-check-value mono">{value}</span>
    </li>
  );
};

const KeyRows: FC<{ encryption: EncryptionKey }> = ({ encryption }) => (
  <ul class="ops-rows">
    <li class="ops-row ops-setting">
      <span class="ops-row-label">key</span>
      <span class="ops-check-value mono">{encryption.key}</span>
      <SourceBadge source={encryption.source} />
    </li>

    <KeyRow name="key_id" value={encryption.key_id} />
    <KeyRow name="fingerprint" value={encryption.fingerprint} />
    <KeyRow name="created_at" value={encryption.created_at} />
  </ul>
);

function settingsHint(state: SettingsState): string {
  if (state.older) return 'This daemon is older than 1.21: it has no stored settings. A log change lasts until a restart.';

  if (state.forbidden) return 'This token has no admin of *, so the daemon does not show its settings. Use a token with admin of *, or the unix socket.';

  return state.error ?? 'The daemon did not give its settings.';
}

// SettingsCard shows GET /_cluster/settings: the encryption key, masked,
// and the log settings with where each one comes from.
const SettingsCard: FC<{ state: SettingsState }> = ({ state }) => {
  const settings = state.settings;

  if (!settings) {
    return (
      <Card class="ops-settings">
        <h2 class="card-title">Settings</h2>
        <span class="hint">{settingsHint(state)}</span>
      </Card>
    );
  }

  const log = settings.log ?? {};

  return (
    <Card class="ops-settings">
      <div class="dash-card-head">
        <h2 class="card-title">Settings</h2>
        <span class="dash-card-note mono">/_cluster/settings</span>
      </div>

      <div class="ops-settings-grid">
        <div>
          <h3 class="ops-settings-title">Encryption key</h3>

          {settings.encryption ? <KeyRows encryption={settings.encryption} /> : <span class="hint">The daemon gave no key.</span>}

          <span class="hint">The console shows the key masked. The daemon never gives the whole key.</span>
        </div>

        <div>
          <h3 class="ops-settings-title">Log settings</h3>

          <ul class="ops-rows">
            <SettingRow name="level" setting={log.level} />
            <SettingRow name="body_max" setting={log.body_max} />
            <SettingRow name="slow_ms" setting={log.slow_ms} />
          </ul>

          <span class="hint">A value from ndb.yml, the env or a flag wins over a stored value at the next start.</span>
        </div>
      </div>
    </Card>
  );
};

const UseRow: FC<{ name: string; text?: string }> = ({ name, text }) => {
  if (text === undefined) return null;

  return (
    <li class="ops-row">
      <span class="ops-row-label mono">{name}</span>
      <span class="ops-check-value">{text}</span>
    </li>
  );
};

// UseCard is the Limits card. The daemon gives the current use in /_metrics
// but not the limits of ndb.yml, so the rows have no bar.
const UseCard: FC<{ metrics: Metrics }> = ({ metrics }) => {
  const watches = metrics.get('ndb_watch_streams');
  const memtable = metrics.get('ndb_memtable_bytes');
  const blobs = metrics.get('ndb_blob_store_bytes');

  return (
    <Card>
      <h2 class="card-title">Limits</h2>

      <ul class="ops-rows">
        <UseRow name="watch streams" text={watches === undefined ? undefined : `${formatCount(watches)} open`} />
        <UseRow name="memtables" text={memtable === undefined ? undefined : formatBytes(memtable)} />
        <UseRow name="blob store" text={blobs === undefined ? undefined : formatBytes(blobs)} />
      </ul>

      <span class="hint">The daemon reports the use, not the limits. max_conns, max_watches, memory_limit and read_cache are in ndb.yml.</span>
    </Card>
  );
};
