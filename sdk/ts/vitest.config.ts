import { readFileSync } from 'node:fs'
import { defineConfig } from 'vitest/config'

// The same define as tsup.config.ts: the tests see the version of
// package.json in src/version.ts.
const pkg = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')) as { version: string }

export default defineConfig({
  define: { __NDB_VERSION__: JSON.stringify(pkg.version) },
  test: {
    pool: 'forks',
    maxWorkers: 2,
    globalSetup: ['./test/global-setup.ts'],
    testTimeout: 20000,
    hookTimeout: 120000,
    typecheck: { enabled: true, include: ['test/**/*.test-d.ts'] },
  },
})
