import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const markers = /^\s*(?:\/\/|#)\s?#?(?:end)?region(?:\s+[\w*-]+)?\s*$/
const labelOf = /\[(.+?)\]\s*$/
const fenceLang: Record<string, string> = { go: 'go', rb: 'ruby', ts: 'ts', sh: 'bash', yml: 'yaml', yaml: 'yaml', txt: 'text' }

function dedent(lines: string[]): string[] {
  const indents = lines.filter((line) => line.trim() !== '').map((line) => /^\s*/.exec(line)![0].length)
  const cut = indents.length > 0 ? Math.min(...indents) : 0

  return lines.map((line) => line.slice(cut))
}

// Reads the file of one `<<< @/path#region{lang} [Title]` line, like VitePress does.
function snippetBody(repoRoot: string, spec: string): { path: string; text: string; label?: string; lang: string } | null {
  const label = labelOf.exec(spec)?.[1]
  const match = /^@\/([^#{\s[]+)(?:#([\w-]+))?/.exec(spec.replace(labelOf, '').trim())

  if (!match) {
    return null
  }

  const [, path, region] = match
  const file = resolve(repoRoot, path)

  if (!existsSync(file)) {
    return null
  }

  let lines = readFileSync(file, 'utf8').replace(/\r\n/g, '\n').replace(/\n$/, '').split('\n')

  if (region) {
    const start = lines.findIndex((line) => new RegExp(`^\\s*(?:\\/\\/|#)\\s?#?region\\s+${region}\\s*$`).test(line))
    const end = lines.findIndex((line, i) => i > start && new RegExp(`^\\s*(?:\\/\\/|#)\\s?#?endregion\\s+${region}\\s*$`).test(line))

    if (start >= 0 && end > start) {
      lines = dedent(lines.slice(start + 1, end).filter((line) => !markers.test(line)))
    }
  }

  const ext = path.split('.').pop() ?? ''

  return { path, text: lines.join('\n'), label, lang: fenceLang[ext] ?? ext }
}

// The raw page for people and LLMs (llms-full.txt, "Copy page", the .md files):
// `<<<` imports become real code blocks, and the code-group markers go away.
export function expandSnippets(markdown: string, repoRoot: string): string {
  return markdown
    .split('\n')
    .flatMap((line) => {
      if (/^:::\s*code-group\s*$/.test(line) || line.trim() === ':::') {
        return []
      }

      const spec = /^<<<\s+(.+)$/.exec(line)?.[1]
      const snippet = spec ? snippetBody(repoRoot, spec) : null

      if (!snippet) {
        return [line]
      }

      const caption = snippet.label ? `**${snippet.label}** (\`${snippet.path}\`)` : `\`${snippet.path}\``

      return [caption, '', '```' + snippet.lang, snippet.text, '```', '']
    })
    .join('\n')
}
