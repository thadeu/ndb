import { raw } from 'hono/html';
import type { Child, FC } from 'hono/jsx';

import { prefix } from '@/prefix';

import { Dot } from '@/ui/badge';
import { Icon, type IconName } from '@/ui/icon';
import { Logo, Wordmark } from '@/ui/logo';
import { formatVersion } from '@/format';

export type Tab = 'overview' | 'explorer' | 'streams' | 'aggregate' | 'watch' | 'devtools' | 'backups' | 'operations';

export interface Current {
  name: string;
  url: string;
  online: boolean;
  version?: string;
  uptime?: string;
}

interface NavItem {
  id: Tab;
  label: string;
  path: string;
  icon: IconName;
}

const GROUPS: { label: string; items: NavItem[] }[] = [
  {
    label: 'Data',
    items: [
      { id: 'overview', label: 'Dashboard', path: '', icon: 'dashboard' },
      { id: 'explorer', label: 'Collections', path: '/explorer', icon: 'database' },
      { id: 'streams', label: 'Streams', path: '/streams', icon: 'streams' },
    ],
  },
  {
    label: 'Query',
    items: [
      { id: 'aggregate', label: 'Aggregate', path: '/aggregate', icon: 'chart' },
      { id: 'watch', label: 'Watch', path: '/watch', icon: 'eye' },
      { id: 'devtools', label: 'Dev Tools', path: '/devtools', icon: 'code' },
    ],
  },
  {
    label: 'Operate',
    items: [
      { id: 'backups', label: 'Backups', path: '/backups', icon: 'history' },
      { id: 'operations', label: 'Operations', path: '/operations', icon: 'sliders' },
    ],
  },
];

// The sidebar and the collection list can fold to a rail. This line runs
// before the first paint and puts the saved choices on <html>, so the page
// does not jump.
const RAIL_SCRIPT = `<script>try{for(const k of ['side','list'])if(localStorage.getItem('ndb-ui:'+k)==='rail')document.documentElement.dataset[k]='rail'}catch(e){}</script>`;

export interface LayoutProps {
  title: string;
  current?: Current;
  tab?: Tab;
  scripts?: string[];
  bleed?: boolean;
  children?: Child;
}

// Layout is the page shell. With a connection it is a sidebar (the brand,
// the connection switcher, the sections) next to the page body. Without one
// it is a top bar with the brand. Every link is relative to the <base> tag,
// so a mount path works.
export const Layout: FC<LayoutProps> = ({ title, current, tab, scripts = [], bleed = false, children }) => {
  const bodyClass = bleed ? 'body bleed' : 'body';

  return (
    <>
      {raw('<!doctype html>')}
      <html lang="en">
        <head>
          <meta charset="utf-8" />
          <meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no, viewport-fit=cover" />
          <meta name="theme-color" content="#ffffff" />
          <meta name="mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-capable" content="yes" />
          <meta name="apple-mobile-web-app-status-bar-style" content="default" />
          <meta name="apple-mobile-web-app-title" content="ndb" />
          <meta name="format-detection" content="telephone=no" />
          <base href={`${prefix()}/`} />
          <title>{`${title} · Ndb Console`}</title>
          <link rel="icon" href="assets/favicon.svg" type="image/svg+xml" />
          <link rel="apple-touch-icon" href="assets/apple-touch-icon.png" />
          <link rel="manifest" href="manifest.webmanifest" />
          {raw(RAIL_SCRIPT)}
          <link rel="stylesheet" href="assets/date-picker.css" />
          <link rel="stylesheet" href="assets/app.css" />
          <script type="module" src="assets/ui.js"></script>
          {scripts.map(name => (
            <script type="module" src={`assets/${name}.js`}></script>
          ))}
        </head>

        {current ? (
          <body class="shell">
            <Sidebar current={current} active={tab} />

            <main class={bodyClass}>{children}</main>
          </body>
        ) : (
          <body>
            <header class="top">
              <Brand />
            </header>

            <main class={bodyClass}>{children}</main>
          </body>
        )}
      </html>
    </>
  );
};

const Brand: FC = () => (
  <a class="brand" href="./" aria-label="Ndb Console">
    <Logo />

    <span class="wordmark">
      <Wordmark />
    </span>
    <span class="pill">console</span>
  </a>
);

// Sidebar holds the navigation. On a narrow screen it is a bar at the top,
// and the menu button (a checkbox, so it needs no script) shows the links.
// On a wide screen the fold button makes it a rail of icons.
const Sidebar: FC<{ current: Current; active?: Tab }> = ({ current, active }) => {
  const base = `c/${encodeURIComponent(current.name)}`;
  const version = current.version ? `ndb ${formatVersion(current.version)}` : 'offline';
  const uptime = current.uptime ? ` · up ${current.uptime}` : '';

  return (
    <aside class="side">
      <div class="side-head">
        <Brand />

        <label class="side-menu btn small icon-only" for="side-toggle" aria-label="Menu">
          <Icon name="menu" />
        </label>

        <button type="button" class="side-collapse btn small icon-only ghost" data-collapse="side" aria-label="Fold or open the sidebar">
          <Icon name="sidebar" />
        </button>
      </div>

      <input class="side-toggle" type="checkbox" id="side-toggle" aria-hidden="true" tabindex={-1} />

      <div class="side-fold">
        <a class="switcher" href="./" aria-label="Switch connection">
          <Dot on={current.online} />

          <span class="switcher-text">
            <span class="switcher-name">{current.name}</span>
            <span class="switcher-url mono">{current.url}</span>
          </span>

          <span class="chev">
            <Icon name="chevrons-up-down" size={16} />
          </span>
        </a>

        <nav class="side-nav" aria-label="Sections">
          {GROUPS.map(group => (
            <div class="side-group">
              <span class="side-label">{group.label}</span>

              {group.items.map(item => (
                <a
                  class={item.id === active ? 'side-link on' : 'side-link'}
                  href={`${base}${item.path}`}
                  title={item.label}
                  aria-current={item.id === active ? 'page' : undefined}
                >
                  <Icon name={item.icon} />
                  <span class="side-text">{item.label}</span>
                </a>
              ))}
            </div>
          ))}
        </nav>

        <div class="side-foot mono">
          {version}
          {uptime}
        </div>
      </div>
    </aside>
  );
};
