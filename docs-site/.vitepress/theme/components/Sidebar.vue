<script setup lang="ts">
import { computed } from 'vue'
import { useData, useRoute, withBase } from 'vitepress'

import type { Tab } from '../../nav'
import { useActiveAnchor } from '../composables/useActiveAnchor'

const props = defineProps<{ tab: Tab; open: boolean }>()

defineEmits<{ (e: 'close'): void }>()

const route = useRoute()
const { theme } = useData()

// siteLinks are the top bar links. The top bar hides them on phones, so the
// drawer shows them at the end.
const repo = 'https://github.com/thadeu/ndb'
const siteLinks = [
  { text: 'Docs', link: '/architecture' },
  { text: 'Guides', link: '/clients' },
  { text: 'Reference', link: '/http-api' },
  { text: 'UI', link: '/ui' },
]
const anchor = useActiveAnchor()

const current = computed(() => {
  const path = route.path.replace(/\.html$/, '').replace(/\/$/, '')

  return path + (route.hash ?? '')
})

const activeLink = computed(() => {
  const items = props.tab.groups.flatMap((group) => group.items)
  const path = current.value.split('#')[0]
  const onPage = items.filter((item) => item.link.split('#')[0] === path)

  if (onPage.length === 0) {
    return ''
  }

  const byAnchor = onPage.find((item) => item.link.split('#')[1] === anchor.value)

  if (byAnchor) {
    return byAnchor.link
  }

  return activeByOrder(onPage.map((item) => item.link)) ?? onPage[0].link
})

// When the visible heading has no sidebar item, keep the last item whose
// anchor sits above it on the page.
function activeByOrder(links: string[]): string | undefined {
  if (typeof document === 'undefined' || !anchor.value) {
    return undefined
  }

  const target = document.getElementById(anchor.value)

  if (!target) {
    return undefined
  }

  let found: string | undefined

  for (const link of links) {
    const id = link.split('#')[1]
    const node = id ? document.getElementById(id) : null

    const above = !id || (node && node.compareDocumentPosition(target) & Node.DOCUMENT_POSITION_FOLLOWING) || node === target

    if (above) {
      found = link
    }
  }

  return found
}
</script>

<template>
  <aside class="sidebar" :class="{ open }" aria-label="Documentation">
    <section v-for="group in tab.groups" :key="group.text" class="side-group">
      <h4 class="side-title">{{ group.text }}</h4>

      <a
        v-for="item in group.items"
        :key="item.link"
        class="side-item"
        :class="{ active: item.link === activeLink }"
        :href="withBase(item.link)"
        @click="$emit('close')"
      >
        {{ item.text }}
      </a>
    </section>

    <section class="side-group side-site">
      <h4 class="side-title">ndb</h4>

      <a v-for="item in siteLinks" :key="item.link" class="side-item" :href="withBase(item.link)" @click="$emit('close')">
        {{ item.text }}
      </a>

      <a class="side-item" :href="repo" target="_blank" rel="noopener">GitHub</a>

      <a v-if="theme.version" class="side-item" :href="`${repo}/blob/main/CHANGELOG.md`" target="_blank" rel="noopener">
        Changelog · v{{ theme.version }}
      </a>
    </section>
  </aside>
</template>
