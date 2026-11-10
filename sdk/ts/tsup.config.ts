import { readFileSync } from 'node:fs'
import { defineConfig } from 'tsup'

// The version of package.json goes in the code as __NDB_VERSION__ (see
// src/version.ts), for the User-Agent.
const pkg = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')) as { version: string }

// ioredis and node-redis are subpaths of the package (ndb/ioredis,
// ndb/node-redis): the main entry does not load them. splitting puts the
// code that the entries share (the error classes) in one chunk, also in CJS,
// so `instanceof NdbError` works across the entries.
export default defineConfig({
  entry: ['src/index.ts', 'src/ioredis.ts', 'src/node-redis.ts'],
  format: ['esm', 'cjs'],
  dts: true,
  clean: true,
  splitting: true,
  target: 'node24',
  define: { __NDB_VERSION__: JSON.stringify(pkg.version) },
})
