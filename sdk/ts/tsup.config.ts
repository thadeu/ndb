import { defineConfig } from 'tsup'

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
})
