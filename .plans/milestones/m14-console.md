# M14 — Ndb Console

Status: done (E14.1 to E14.6 built; release job not yet run) · Depends on: M07, M13 · Design: canvas artifact "Ndb Console" (Connections, Overview, Browser, Dev Tools)

## Goal

A self-hosted web console for ndb, like RedisInsight: open a local socket or a
remote daemon (TCP + token), see the collections, browse and edit documents as
a JSON tree, and send raw HTTP requests in Dev Tools. One binary, a Docker
image, and later Homebrew. A hosted version (cloud.ndb.clowk.in) comes only
after the idea is validated.

## Stack (same pattern as `~/code/anway-webui`)

- Bun 1.4+, Hono, server-rendered `hono/jsx`. Only `hono` at runtime.
- `bun build --compile` gives one binary with CSS and islands inside.
- Folder `console/` in this repo:
  - `src/views/`: one file per page.
  - `src/components/ui/`: design system, one file per component, shadcn names; gallery at `/ui`.
  - `src/components/html/`: layout only (page shell, header, tabs, panels).
  - `src/components/`: other components and islands (`json-tree.tsx`, `devtools.tsx`, `watch.tsx`).
  - `src/hooks/`: `useXxx.ts`, one per file, when a component has effect logic.
  - `src/assets/app.css`: CSS variables on `:root` (LP look: white, ink `#0b0b0f`, pink `#f0047f`, Inter + JetBrains Mono). No CSS framework.
  - Aliases `@/ui`, `@/html`, `@/components`, `@/hooks`, `@/views`, `@/*`.
- Islands export `mount()`; the server renders a usable fallback first.

## Rules

- The browser never gets a token. The console server calls ndb and keeps tokens.
- ndb connection: `unix://path` (Bun `fetch` with `unix:`) or `tcp://host:port` / `http(s)://` with `Authorization: Bearer`.
- Listens on `127.0.0.1:4321` by default. No login yet: do not expose it on a public network.
- Writes from the browser are POST routes on the console with an `Origin` check.
- URL first: collection, filter, selected `_id` live in the query string.

## Epics

### E14.1 Skeleton + Connections

- [x] `console/` with `package.json`, `tsconfig.json`, `CLAUDE.md`, `README.md`, `Dockerfile` (distroless, multi-arch)
- [x] `src/ndb.ts`: client for one connection (unix or tcp, token), typed errors
- [x] Connection store: `~/.config/ndb/console.json` (mode 0600); env `NDB_URL` + `NDB_TOKEN` gives one fixed connection
- [x] Layout (header with bowl logo, connection switcher, tabs), CSS vars, base ui components
- [x] Connections page: cards with status (`/health`, `/v1/stats`), New connection form with Test
- [x] Tests against a real ndb (`NDB_BIN=... bun test`)

### E14.2 Overview

- [x] Stat cards from `/v1/stats`; collections table (write mode, rows, size, indexes, ttl, watchers)
- [x] Disk bar from `/v1/size`

### E14.3 Browser

- [x] Collections sidebar, `where` filter, list with `_id`, `_v`, one-line preview, `X-Ndb-Next` paging
- [x] JSON tree island (Tree / JSON toggle, expand, edit a value)
- [x] Save as `$set`/`$unset` diff on `/{_id}/update` with `_v` guard; Insert; Delete with confirm

### E14.4 Dev Tools

- [x] Request editor island: `METHOD /path` + JSON body blocks, run the block under the cursor (⌘ Enter)
- [x] Response: status, time, `X-Ndb-*` headers, Tree / Raw / Headers; NDJSON as one node per line
- [x] History per connection; Copy as curl

### E14.5 Watch + Backups

- [x] Watch: live stream (SSE from the console), pause, filter
- [x] Backups: `ndb backup` over the socket only; download (no file list: ndb has no backup catalog)

### E14.6 Release

- [x] Release job: `ndb-ui` binaries (darwin/linux, arm64/x64) + `ghcr.io/thadeu/ndb-ui`
- [x] `install.sh` option for the console; docs page; CHANGELOG

## Exit criteria

- `docker run -p 4321:4321 ghcr.io/thadeu/ndb-ui` opens a remote ndb with its token.
- The binary opens `unix:///run/ndb/ndb.sock` with no config.
- Edit of a document in the Browser keeps a concurrent write (guard on `_v`).

## Out of scope

- Login, users, hosted multi-tenant mode (later, like anway-webui proxy mode).
- Homebrew formula (after the first release).
