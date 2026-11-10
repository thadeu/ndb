import { VERSION } from './version.js'

interface RuntimeGlobals {
  Bun?: { version?: string }
  Deno?: { version?: { deno?: string }, build?: { os?: string } }
  process?: { versions?: { node?: string }, platform?: string }
}

// runtimeOf names the JavaScript runtime and the platform: "bun v1.3.2",
// "deno v2.5.0" or "node v24.1.0", and "darwin" or "linux". It is undefined
// in a browser, which has none of them.
export function runtimeOf(g: RuntimeGlobals = globalThis as RuntimeGlobals): { runtime: string, platform: string } | undefined {
  const platform = g.process?.platform ?? g.Deno?.build?.os ?? 'unknown'

  if (g.Bun?.version) {
    return { runtime: `bun v${g.Bun.version}`, platform }
  }

  if (g.Deno?.version?.deno) {
    return { runtime: `deno v${g.Deno.version.deno}`, platform }
  }

  if (g.process?.versions?.node) {
    return { runtime: `node v${g.process.versions.node}`, platform }
  }

  return undefined
}

// userAgent builds the User-Agent header: the SDK, its version, the runtime
// and the platform, then the token of the app, if it has one. A browser
// cannot set a User-Agent: there it is undefined and no header is sent.
export function userAgent(suffix?: string, g?: RuntimeGlobals): string | undefined {
  const rt = runtimeOf(g)

  if (!rt) {
    return undefined
  }

  const app = suffix?.trim()
  const sdk = `ndb-ts/${VERSION} (${rt.runtime}; ${rt.platform})`

  return app ? `${sdk} ${app}` : sdk
}
