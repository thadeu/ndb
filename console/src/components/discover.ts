import { type DiscoverView, type Found, useDiscover } from '@/hooks/useDiscover';
import { formatVersion } from '@/format';

export function describe(found: Found): string {
  if (found.state === 'needs_token') return 'needs a token';

  if (found.state === 'starting') return 'starting';

  const parts = ['ok'];

  if (found.version) parts.push(formatVersion(found.version));

  if (found.collections !== undefined) parts.push(`${found.collections} ${found.collections === 1 ? 'collection' : 'collections'}`);

  return parts.join(' · ');
}

// suggestName is "local" for a unix socket, and the host for TCP.
export function suggestName(url: string): string {
  if (url.startsWith('unix://')) return 'local';

  return url.replace(/^[a-z]+:\/\//, '').replace(/[/:].*$/, '').replace(/[^A-Za-z0-9._-]/g, '');
}

// splitTarget reads the URL field as a Discover target: a full tcp:// URL or
// host:port gives the host and its port, a unix:// URL or an empty field means
// this machine.
export function splitTarget(value: string): { host: string; ports: string } {
  const text = value.trim().replace(/^(tcp|https?):\/\//, '').replace(/\/.*$/, '');

  if (text === '' || value.trim().startsWith('unix://')) return { host: '', ports: '' };

  const match = /^(\[[^\]]+\]|[^:]+)(?::(\d+))?$/.exec(text);

  if (!match) return { host: text, ports: '' };

  return { host: match[1]!.replace(/^\[|\]$/g, ''), ports: match[2] ?? '' };
}

function element(tag: string, className: string, text: string): HTMLElement {
  const node = document.createElement(tag);

  node.className = className;
  node.textContent = text;

  return node;
}

const TONES: Record<Found['state'], string> = { ok: 'ok', needs_token: 'pink', starting: 'neutral' };

// resultItem is one found daemon: the whole row is a button that fills the
// form. It shows the kind (socket or TCP), the URL, and the state.
function resultItem(found: Found, onUse: () => void): HTMLElement {
  const item = element('li', '', '');
  const row = element('button', `discover-item ${TONES[found.state]}`, '') as HTMLButtonElement;
  const body = element('span', 'discover-body', '');
  const meta = element('span', 'discover-meta', '');

  row.type = 'button';
  row.addEventListener('click', () => {
    item.parentElement?.querySelectorAll('.discover-item.on').forEach(node => node.classList.remove('on'));
    row.classList.add('on');
    onUse();
  });

  meta.append(element('span', 'discover-dot', ''), document.createTextNode(describe(found)));
  body.append(element('span', 'discover-url', found.url), meta);

  row.append(element('span', 'discover-kind', found.url.startsWith('unix://') ? 'sock' : 'tcp'), body, element('span', 'discover-use', 'Use →'));
  item.append(row);

  return item;
}

// mount upgrades each Discover row of the connection forms (New and Edit).
// It needs the script: without it, a note asks for the URL by hand.
export function mount(): void {
  for (const root of document.querySelectorAll<HTMLElement>('[data-discover]')) mountOne(root);
}

function mountOne(root: HTMLElement): void {
  const button = root.querySelector<HTMLButtonElement>('[data-discover-run]');
  const host = root.querySelector<HTMLInputElement>('[data-discover-host]');
  const status = root.querySelector<HTMLElement>('[data-discover-status]');
  const list = root.querySelector<HTMLElement>('[data-discover-list]');

  if (!button || !host || !status || !list) return;

  const form = host.form;
  const name = form?.querySelector<HTMLInputElement>('[name=name]');
  const url = host;
  const token = form?.querySelector<HTMLInputElement>('[name=token]');

  const use = (found: Found) => {
    url.value = found.url;

    if (name && name.value === '') name.value = suggestName(found.url);

    (found.state === 'needs_token' ? token : url)?.focus();
  };

  const render = (view: DiscoverView) => {
    status.className = view.phase === 'error' ? 'test-result bad' : 'test-result';
    status.textContent = view.phase === 'searching' ? 'Searching…' : (view.message ?? (view.phase === 'done' ? (view.hint ?? (view.results.length === 0 ? 'Nothing found.' : '')) : ''));

    list.replaceChildren(...view.results.map(found => resultItem(found, () => use(found))));
  };

  const discover = useDiscover('connections/discover', render);

  const search = () => {
    const target = splitTarget(host.value);

    discover.run(target.host, target.ports);
  };

  button.addEventListener('click', search);
}
