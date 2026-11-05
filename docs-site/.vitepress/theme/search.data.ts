import { createContentLoader } from 'vitepress'

export interface SearchEntry {
  url: string
  page: string
  heading: string
  excerpt: string
  haystack: string
}

declare const data: SearchEntry[]

export { data }

function slug(heading: string): string {
  return heading
    .toLowerCase()
    .replace(/[`*]/g, '')
    .replace(/[^\p{L}\p{N}\s-]/gu, '')
    .trim()
    .replace(/\s+/g, '-')
}

function split(url: string, src: string): SearchEntry[] {
  const entries: SearchEntry[] = []
  let page = ''
  let heading = ''
  let anchor = ''
  let buffer: string[] = []

  const flush = () => {
    const text = buffer.join(' ').replace(/[`|*>#]/g, ' ').replace(/\s+/g, ' ').trim()

    if (!heading) {
      return
    }

    entries.push({
      url: anchor ? `${url}#${anchor}` : url,
      page,
      heading,
      excerpt: text.slice(0, 110),
      haystack: `${heading} ${text}`.toLowerCase(),
    })
  }

  for (const line of src.split('\n')) {
    const match = /^(#{1,3})\s+(.*)$/.exec(line)

    if (!match) {
      buffer.push(line)

      continue
    }

    flush()
    buffer = []
    heading = match[2].replace(/`/g, '')
    anchor = match[1] === '#' ? '' : slug(match[2])

    if (match[1] === '#') {
      page = heading
    }
  }

  flush()

  return entries
}

export default createContentLoader('docs/dev/*.md', {
  includeSrc: true,
  render: false,
  excerpt: false,
  transform(rows) {
    return rows.flatMap((row) => split(row.url, row.src ?? ''))
  },
})
