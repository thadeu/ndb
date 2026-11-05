import { execSync } from 'node:child_process'
import { copyFileSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { dirname, posix, resolve, sep } from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { defineConfig, type Plugin } from 'vitepress'

import { tabs } from './nav'

const here = dirname(fileURLToPath(import.meta.url))
const brand = resolve(here, '../../brand')
const repoRoot = resolve(here, '../..').split(sep).join('/')
const require = createRequire(import.meta.url)
const repoUrl = 'https://github.com/thadeu/ndb'
const brandFiles = ['svg/favicon.svg', 'favicon.ico', 'png/apple-touch-icon.png']

// The docs version: DOCS_VERSION (set by CI from the latest release tag),
// else the latest git tag, else the SDK package version.
function docsVersion(): string {
  const fromEnv = process.env.DOCS_VERSION?.trim()

  if (fromEnv) {
    return fromEnv.replace(/^v/, '')
  }

  try {
    return execSync('git describe --tags --abbrev=0 --match "v*"', { cwd: here, stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
      .trim()
      .replace(/^v/, '')
  } catch {
    const pkg = JSON.parse(readFileSync(resolve(here, '../../sdk/ts/package.json'), 'utf8')) as { version: string }

    return pkg.version
  }
}

// llms.txt (index) and llms-full.txt (every page), built from docs/dev so
// they never drift from the site. Each page is also served as raw .md.
const docsDir = resolve(here, '../../docs/dev')
const siteUrl = (process.env.DOCS_URL ?? '').replace(/\/$/, '')

interface DocPage {
  slug: string
  title: string
  summary: string
  body: string
}

function docPages(): DocPage[] {
  const files = readdirSync(docsDir).filter((name) => name.endsWith('.md'))

  const navOrder = tabs
    .flatMap((tab) => tab.groups.flatMap((group) => group.items))
    .map((item) => item.link.split('#')[0].replace(/^\//, ''))

  const order = [...new Set([...navOrder, ...files.map((name) => name.replace(/\.md$/, ''))])]

  return order
    .filter((slug) => files.includes(`${slug}.md`))
    .map((slug) => {
      const body = readFileSync(resolve(docsDir, `${slug}.md`), 'utf8')
      const title = /^#\s+(.+)$/m.exec(body)?.[1]?.trim() ?? slug
      const summary = body
        .split(/\n\s*\n/)
        .map((block) => block.trim())
        .find((block) => block && !block.startsWith('#') && !block.startsWith('```') && !block.startsWith('|') && !block.startsWith('<') && !block.startsWith('-') && !block.startsWith('>') && !block.endsWith(':'))
        ?.replace(/\s+/g, ' ') ?? ''

      return { slug, title, summary, body }
    })
}

function writeLlms(outDir: string, version: string) {
  const pages = docPages()

  const header = [
    '# ndb',
    '',
    `> Drop NDJSON. Query it. NoSQL. One Go daemon (version ${version}) that stores NDJSON as its WAL and answers NoSQL JSON queries over a unix socket or TCP, with Go, Ruby (ndb, ndb-rails) and TypeScript (@thadeu/ndb) SDKs.`,
    '',
  ]

  const index = [
    ...header,
    '## Docs',
    '',
    ...pages.map((page) => `- [${page.title}](${siteUrl}/${page.slug}.md): ${page.summary.slice(0, 200)}`),
    '',
    '## Optional',
    '',
    `- [Full docs in one file](${siteUrl}/llms-full.txt)`,
    '- [Source code](https://github.com/thadeu/ndb)',
    '',
  ]

  const full = [...header, ...pages.map((page) => `${page.body.trim()}\n`)].join('\n')

  writeFileSync(resolve(outDir, 'llms.txt'), index.join('\n'))
  writeFileSync(resolve(outDir, 'llms-full.txt'), full)

  for (const page of pages) {
    writeFileSync(resolve(outDir, `${page.slug}.md`), page.body)
  }
}

const themeScript = `
try {
  var t = localStorage.getItem('ndb-theme')
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t)
} catch (e) {}
`

function githubTarget(path: string): string {
  const kind = path.endsWith('/') ? 'tree' : 'blob'

  return `${repoUrl}/${kind}/main/${path.replace(/\/$/, '')}`
}

function brandAssets(): Plugin {
  return {
    name: 'ndb-brand-assets',

    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const name = brandFiles.find((f) => req.url?.split('?')[0].endsWith(`/${posix.basename(f)}`))

        if (!name) {
          next()

          return
        }

        res.setHeader('Content-Type', name.endsWith('.svg') ? 'image/svg+xml' : 'image/png')
        res.end(readFileSync(resolve(brand, name)))
      })
    },
  }
}

export default defineConfig({
  title: 'ndb',
  description: 'Drop NDJSON. Query it. NoSQL. One Go daemon with a JSON query API.',
  lang: 'en-US',
  base: process.env.DOCS_BASE ?? '/',
  srcDir: '..',
  cleanUrls: true,
  appearance: false,
  lastUpdated: false,

  themeConfig: {
    version: docsVersion(),
  },

  srcExclude: [
    'README.md',
    'CONTRIBUTING.md',
    'CODE_OF_CONDUCT.md',
    'SECURITY.md',
    '.plans/**',
    'brand/**',
    'sdk/**',
    'deploy/**',
    'spec/**',
    'test/**',
    'docs-site/node_modules/**',
    'docs-site/README.md',
  ],

  rewrites: {
    'docs/dev/:page.md': ':page.md',
    'docs-site/landing.md': 'index.md',
  },

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' }],
    ['link', { rel: 'alternate icon', href: '/favicon.ico' }],
    ['link', { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' }],
    ['meta', { name: 'theme-color', content: '#f0047f' }],
    ['script', {}, themeScript],
  ],

  markdown: {
    theme: { light: 'github-light', dark: 'github-dark' },
    headers: { level: [2, 3] },

    config(md) {
      md.core.ruler.push('ndb-repo-links', (state) => {
        const source = posix.relative(repoRoot, String(state.env.path ?? '').split(sep).join('/'))

        for (const block of state.tokens) {
          for (const token of block.children ?? []) {
            if (token.type !== 'link_open') {
              continue
            }

            const href = token.attrGet('href') ?? ''

            if (!href.startsWith('.')) {
              continue
            }

            const target = posix.normalize(posix.join(posix.dirname(source), href))

            if (!target.startsWith('docs/dev/')) {
              token.attrSet('href', githubTarget(target))
              token.attrSet('target', '_blank')
              token.attrSet('rel', 'noopener')
            }
          }
        }
      })
    },
  },

  transformPageData(pageData, { siteConfig }) {
    const source = siteConfig.rewrites.inv[pageData.relativePath] ?? pageData.relativePath
    const file = resolve(here, '../..', source)

    pageData.frontmatter.raw = readFileSync(file, 'utf8')
  },

  buildEnd(config) {
    for (const name of brandFiles) {
      const target = resolve(config.outDir, posix.basename(name))

      mkdirSync(dirname(target), { recursive: true })
      copyFileSync(resolve(brand, name), target)
    }

    writeLlms(config.outDir, docsVersion())
  },

  vite: {
    plugins: [brandAssets()],

    resolve: {
      alias: [
        { find: /^vue\/server-renderer$/, replacement: require.resolve('vue/server-renderer') },
        { find: /^vue$/, replacement: require.resolve('vue/dist/vue.runtime.esm-bundler.js') },
      ],
    },

    server: { fs: { allow: ['../..'] } },
  },
})
