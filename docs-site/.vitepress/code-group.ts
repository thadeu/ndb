import type { MarkdownRenderer } from 'vitepress'

type BlockRule = Parameters<MarkdownRenderer['block']['ruler']['push']>[1]

const opener = /^:::\s*code-group\s*$/
const title = /\[(.+?)\]/

// `::: code-group` ... `:::` wraps code fences (or `<<<` imports) into tabs.
// The tab label is the [Title] of each fence. The panes are plain code blocks;
// the client script (composables/useCodeTabs.ts) switches data-active.
export function codeGroup(md: MarkdownRenderer) {
  const rule: BlockRule = (state, startLine, endLine, silent) => {
    const first = state.bMarks[startLine] + state.tShift[startLine]

    if (!opener.test(state.src.slice(first, state.eMarks[startLine]))) {
      return false
    }

    if (silent) {
      return true
    }

    let close = startLine + 1

    while (close < endLine) {
      const from = state.bMarks[close] + state.tShift[close]

      if (state.src.slice(from, state.eMarks[close]).trim() === ':::') {
        break
      }

      close++
    }

    const open = state.push('code_group_open', 'div', 1)

    open.map = [startLine, close]

    const parentType = state.parentType
    const lineMax = state.lineMax

    state.parentType = 'container' as typeof state.parentType
    state.lineMax = close

    state.md.block.tokenize(state, startLine + 1, close)

    state.parentType = parentType
    state.lineMax = lineMax

    state.push('code_group_close', 'div', -1)

    state.line = close + 1

    return true
  }

  // VitePress has its own code-group container; ours must run first.
  md.block.ruler.before('container_code-group', 'code_group', rule, { alt: ['paragraph', 'reference', 'blockquote', 'list'] })

  md.renderer.rules.code_group_open = (tokens, index) => {
    const labels: string[] = []

    for (let i = index + 1; i < tokens.length && tokens[i].type !== 'code_group_close'; i++) {
      if (tokens[i].type === 'fence') {
        labels.push(title.exec(tokens[i].info)?.[1] ?? tokens[i].info.trim().split(/\s+/)[0])
      }
    }

    const buttons = labels
      .map((label, i) => {
        const safe = md.utils.escapeHtml(label)

        return `<button type="button" role="tab" class="code-tab${i === 0 ? ' active' : ''}" aria-selected="${i === 0}" data-label="${safe}">${safe}</button>`
      })
      .join('')

    return `<div class="code-group" data-active="0"><nav class="code-tabs" role="tablist" aria-label="Language">${buttons}</nav>\n`
  }

  md.renderer.rules.code_group_close = () => '</div>\n'
}
