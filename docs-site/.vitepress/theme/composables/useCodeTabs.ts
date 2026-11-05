import { onMounted, onBeforeUnmount } from 'vue'
import { onContentUpdated } from 'vitepress'

const storageKey = 'ndb-code-lang'

function savedLabel(): string | null {
  try {
    return localStorage.getItem(storageKey)
  } catch {
    return null
  }
}

function rememberLabel(label: string) {
  try {
    localStorage.setItem(storageKey, label)
  } catch {
    return
  }
}

// Shows the pane with this label. A group without the label keeps its tab.
function select(group: HTMLElement, label: string) {
  const buttons = Array.from(group.querySelectorAll<HTMLButtonElement>('.code-tab'))
  const index = buttons.findIndex((button) => button.dataset.label === label)

  if (index < 0) {
    return
  }

  group.dataset.active = String(index)

  buttons.forEach((button, i) => {
    button.classList.toggle('active', i === index)
    button.setAttribute('aria-selected', String(i === index))
  })
}

function selectAll(label: string) {
  document.querySelectorAll<HTMLElement>('.code-group').forEach((group) => select(group, label))
}

// One language for the whole site: a click on a tab switches every code group
// on the page, and the choice is kept for the next page and the next visit.
export function useCodeTabs() {
  const onClick = (event: MouseEvent) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>('.code-tab')
    const label = button?.dataset.label

    if (!button || !label) {
      return
    }

    const before = button.getBoundingClientRect().top

    rememberLabel(label)
    selectAll(label)

    window.scrollBy(0, button.getBoundingClientRect().top - before)
  }

  const restore = () => {
    const label = savedLabel()

    if (label) {
      selectAll(label)
    }
  }

  onMounted(() => {
    document.addEventListener('click', onClick)

    restore()
  })

  onBeforeUnmount(() => document.removeEventListener('click', onClick))

  onContentUpdated(restore)
}
