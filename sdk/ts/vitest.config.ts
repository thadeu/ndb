import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    pool: 'forks',
    maxWorkers: 2,
    globalSetup: ['./test/global-setup.ts'],
    testTimeout: 20000,
    hookTimeout: 120000,
    typecheck: { enabled: true, include: ['test/**/*.test-d.ts'] },
  },
})
