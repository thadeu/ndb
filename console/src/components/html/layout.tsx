import { raw } from 'hono/html';
import type { Child, FC } from 'hono/jsx';

import { prefix } from '@/prefix';

import { Dot } from '@/ui/badge';
import { Icon } from '@/ui/icon';
import { Logo } from '@/ui/logo';
import { formatVersion } from '@/format';

export type Tab = 'overview' | 'devtools' | 'watch' | 'backups' | 'explorer';

export interface Current {
  name: string;
  url: string;
  online: boolean;
  version?: string;
  uptime?: string;
}

const TABS: { id: Tab; label: string; path: string }[] = [
  { id: 'overview', label: 'Overview', path: '' },
  { id: 'explorer', label: 'Explorer', path: '/explorer' },
  { id: 'devtools', label: 'Dev Tools', path: '/devtools' },
  { id: 'watch', label: 'Watch', path: '/watch' },
  { id: 'backups', label: 'Backups', path: '/backups' },
];

export interface LayoutProps {
  title: string;
  current?: Current;
  tab?: Tab;
  scripts?: string[];
  bleed?: boolean;
  children?: Child;
}

// Layout is the page shell: header, tabs when a connection is open, and the
// body. Every link is relative to the <base> tag, so a mount path works.
export const Layout: FC<LayoutProps> = ({ title, current, tab, scripts = [], bleed = false, children }) => (
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
        <link rel="stylesheet" href="assets/date-picker.css" />
        <link rel="stylesheet" href="assets/app.css" />
        <script type="module" src="assets/ui.js"></script>
        {scripts.map(name => (
          <script type="module" src={`assets/${name}.js`}></script>
        ))}
      </head>

      <body>
        <Header current={current} />

        {current && tab && <Tabs name={current.name} active={tab} />}

        <main class={bleed ? 'body bleed' : 'body'}>{children}</main>
      </body>
    </html>
  </>
);

const Header: FC<{ current?: Current }> = ({ current }) => {
  const meta = current?.version ? `ndb ${formatVersion(current.version)}${current.uptime ? ` · up ${current.uptime}` : ''}` : undefined;

  return (
    <header class="top">
      <a class="brand" href="./" aria-label="Ndb Console">
        <Logo />
        <span class="wordmark">ndb</span>
        {!current && <span class="pill">console</span>}
      </a>

      {current && <Switcher current={current} />}

      <span class="grow" />

      {meta && <span class="pill mono">{meta}</span>}
    </header>
  );
};

const Switcher: FC<{ current: Current }> = ({ current }) => (
  <a class="switcher" href="./" aria-label="Switch connection">
    <Dot on={current.online} />
    <span class="switcher-name">{current.name}</span>
    <span class="switcher-url mono">{current.url}</span>
    <span class="chev">
      <Icon name="chevron-down" size={16} />
    </span>
  </a>
);

const Tabs: FC<{ name: string; active: Tab }> = ({ name, active }) => (
  <nav class="tabs" aria-label="Sections">
    {TABS.map(item => (
      <a class={item.id === active ? 'tab on' : 'tab'} href={`c/${encodeURIComponent(name)}${item.path}`} aria-current={item.id === active ? 'page' : undefined}>
        {item.label}
      </a>
    ))}
  </nav>
);
