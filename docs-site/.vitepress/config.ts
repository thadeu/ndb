import { copyFileSync, mkdirSync, readFileSync } from 'node:fs'
import { dirname, posix, resolve, sep } from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { defineConfig, type Plugin } from 'vitepress'

const here = dirname(fileURLToPath(import.meta.url))
const brand = resolve(here, '../../brand')
const repoRoot = resolve(here, '../..').split(sep).join('/')
const require = createRequire(import.meta.url)
const repoUrl = 'https://github.com/thadeu/ndb'
const brandFiles = ['svg/favicon.svg', 'favicon.ico', 'png/apple-touch-icon.png']

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
  description: 'A query engine for NDJSON: one Go daemon with a MongoDB-style JSON API.',
  lang: 'en-US',
  base: process.env.DOCS_BASE ?? '/',
  srcDir: '..',
  cleanUrls: true,
  appearance: false,
  lastUpdated: false,

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
