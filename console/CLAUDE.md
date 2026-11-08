# Project Instructions

Web console for the ndb daemon ("Ndb Console"). Bun, Hono, and server-rendered JSX, compiled into one binary (`ndb-ui`). Read `README.md` first. The API contract is the HTTP reference in `docs/dev/http.md` and `docs/dev/http/` in this repo. The plan was `.plans/milestones/m14-console.md` (built; in git history).

## Rules

- **Token:** The browser never receives an ndb token. Pages render on the server, and actions are POST routes on this server. The pages show only "token set".
- **JSX:** Hono JSX, not React, rendered on the server. Put each part in its folder:
  - `src/views/`: pages only, one file for each page.
  - `src/components/ui/`: the design system, one file for each component, named as in the shadcn list. Use these components in pages. Do not write `class="btn"` in a page. The gallery at `/ui` shows each component. Add a new component to the gallery in the same change.
  - `src/components/html/`: layout only, such as the page shell, header, and tabs.
  - `src/components/`: other components and islands.
  - `src/hooks/`: hooks, one in each file, named `useXxx.ts`. Put a hook here when a component has logic with an effect, such as a request.
- **lib/ui:** `lib/ui/` is `@thadeu/ui`, a package of its own for npm (the date picker, the popover): plain TypeScript and DOM, no Hono, its own CSS in `lib/ui/styles/` with `--dp-*` variables, built with tsup (ESM, CJS, types). The console imports it as `@/lib/ui/...` and wraps it in `src/components/ui/` (server markup, theme in `app.css`). Keep it free of console code.
- **Imports:** Use the aliases in `tsconfig.json` between folders: `@/ui/...`, `@/html/...`, `@/components/...`, `@/hooks/...`, `@/views/...`, and `@/...` for other files in `src`, such as `@/ndb`. Use `./` only for a file in the same folder.
- **Browser:** A component with behavior in the browser exports `mount()`, and `src/components/ui/client.ts` calls it. The layout loads that bundle, `ui.js`, on every page. A big island (`document-editor`, `devtools`, `watch`) has its own entry in the `client` build script, a route in `main.ts` (`islands`), and is loaded by the page through the `scripts` prop of the layout. Pure logic of an island goes in a separate `.ts` file with a unit test. The server renders a usable fallback first: a native form works without the script.
- **Design:** Light theme in `src/assets/app.css`. All colors are CSS variables on `:root`, and rules are grouped by part with `/* part: ... */` comments. Do not add a CSS framework. Before you add a class, search for it.
- **Responsive:** Every UI change must work at 360 px: tables in an `overflow-x` wrapper, rows that stack.
- **URL first:** Collection, filter, and selected `_id` live in the query string, so a link shows the same view.
- **Writes:** Every POST route checks `Origin` (the `csrf` middleware in `src/app.tsx`).
- **Prefix:** Pages use relative URLs and a `<base>` tag. `X-Forwarded-Prefix` sets the mount path (`src/prefix.ts`).
- **Doctype:** Every page starts with `<!doctype html>`, through the layout.
- **Runtime:** Bun 1.4 or later (`mise.toml` pins it). The build compiles one binary with the styles and islands inside.
- **Login:** None yet. Do not expose the console on a public network. Default listen address is `127.0.0.1:4321`.
- **Dependencies:** Only `hono` at runtime. Discuss a new one before you add it.

## Tests

```bash
NDB_BIN=/path/to/ndb bun test
```

Tests run against a real ndb daemon. Build it with `go build -o /tmp/ndb-console-test/ndb ./cmd/ndb` in the repo root (the default `NDB_BIN`). Keep runs light: `nice bun test`.

## Code style

- Use one blank line between blocks with different responsibilities: before `if`, `for`, `while`, `switch`, `try`, and `return`, and after a closing `}` when more statements follow.
- Keep lines that build the same value together.
- In JSX: a blank line between sibling blocks with different jobs, one attribute per line on long elements, a computed class or style in a `const` above `return`. No comments inside JSX.
