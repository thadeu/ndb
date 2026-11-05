# M11 — TS client (package `ndb`)

Status: done (light verification) · Depends on: M07 (blob support waits for M12) · Plan: §12.2

## Goal

`ndb.model('calls', schema)` with types inferred from the schema.
No query semantics in the package: it only builds JSON.

## Epics

### E11.1 Project setup

- [ ] pnpm, TypeScript strict, Vitest
- [ ] ESLint flat config (`eslint.config.mjs`) with the house rules: blank lines around `return`/blocks/declarations, `curly: multi-line`, `no-console` (warn/error allowed), `prefer-const`, `no-var`, `no-inline-comments`
- [ ] ESM + CJS build

### E11.2 Transport

- [ ] Tests first: against a real daemon started by the test suite
- [ ] `undici` client on a unix socket, keep-alive
- [ ] NDJSON stream → async iterator; typed errors

### E11.3 Models

- [ ] Tests first: type inference from the schema (type tests); `ensure_collection` on first use
- [ ] `ndb.model(name, { id, time, fields })`
- [ ] `create`, `update` (with `ifVersion`), `delete`, `find(id)`

### E11.4 Query builder

- [ ] `where`, `not`, `or`, `sort`, `limit`, `skip`, `fields`; thenable (await runs it)
- [ ] `count`, `first`, `iterate()` (async iterator)
- [ ] Pagination: `page(n, size)`, `after(token)` + `nextCursor`
- [ ] `aggregate(stages)`

### E11.5 Shared suite

- [ ] Runner for all `spec/cases/`

## Exit criteria

- [ ] All `spec/cases/` pass through the package
- [ ] Type tests pass (wrong field names fail to compile)
- [ ] Node example in `sdk/ts/examples/`

## Out of scope

- Browser support (unix socket only in v1). Associations.
