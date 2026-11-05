import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vitepress'

// The id of the last h2/h3 whose top has scrolled past the sticky bars,
// or '' while the reader is above the first heading.
export function useActiveAnchor() {
  const route = useRoute()
  const active = ref('')

  let frame = 0

  const offset = () => {
    const styles = getComputedStyle(document.documentElement)
    const topbar = parseFloat(styles.getPropertyValue('--topbar-h')) || 64
    const tabs = parseFloat(styles.getPropertyValue('--tabs-h')) || 48

    return topbar + tabs + 24
  }

  const update = () => {
    frame = 0

    const headings = Array.from(document.querySelectorAll<HTMLElement>('.doc-content h2[id], .doc-content h3[id]'))
    const line = offset()

    let current = ''

    for (const heading of headings) {
      if (heading.getBoundingClientRect().top > line) {
        break
      }

      current = heading.id
    }

    const atBottom = window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 2

    if (atBottom && headings.length > 0) {
      current = headings[headings.length - 1].id
    }

    active.value = current
  }

  const schedule = () => {
    if (!frame) {
      frame = requestAnimationFrame(update)
    }
  }

  onMounted(() => {
    window.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('resize', schedule)

    update()
  })

  onBeforeUnmount(() => {
    window.removeEventListener('scroll', schedule)
    window.removeEventListener('resize', schedule)

    cancelAnimationFrame(frame)
  })

  watch(
    () => route.path,
    () => setTimeout(update, 50),
  )

  return active
}
