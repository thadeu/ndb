import css from '@/assets/app.css' with { type: 'file' };
import favicon from '@/assets/favicon.svg' with { type: 'file' };
import devtools from '../dist/devtools.js' with { type: 'file' };
import documentEditor from '../dist/document-editor.js' with { type: 'file' };
import ui from '../dist/ui.js' with { type: 'file' };
import watch from '../dist/watch.js' with { type: 'file' };

import { createApp } from './app';
import { Connections, defaultPath, fixedFromEnv } from './connections';

const port = Number(process.env.PORT ?? 4321);
const hostname = process.env.HOST ?? '127.0.0.1';

const connections = new Connections(defaultPath(), fixedFromEnv());

const script = (path: string) => () => new Response(Bun.file(path), { headers: { 'content-type': 'text/javascript; charset=utf-8' } });

const app = createApp(connections, {
  css: () => new Response(Bun.file(css), { headers: { 'content-type': 'text/css; charset=utf-8' } }),
  ui: script(ui),
  favicon: () => new Response(Bun.file(favicon), { headers: { 'content-type': 'image/svg+xml', 'cache-control': 'public, max-age=86400' } }),
  islands: { devtools: script(devtools), 'document-editor': script(documentEditor), watch: script(watch) },
});

Bun.serve({ port, hostname, development: false, fetch: app.fetch });

console.log(`ndb-ui listening on ${hostname}:${port}`);
