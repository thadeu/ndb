// __NDB_VERSION__ is the "version" of package.json. tsup (the build) and
// vitest (the tests) put it in with `define`. The release flow bumps only
// package.json, so there is no copy to keep in sync.
declare const __NDB_VERSION__: string | undefined

// VERSION is the version of this package, or "dev" when the code runs from
// the source without the define.
export const VERSION: string = typeof __NDB_VERSION__ === 'string' ? __NDB_VERSION__ : 'dev'
