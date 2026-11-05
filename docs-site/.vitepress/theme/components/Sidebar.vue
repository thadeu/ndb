<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, withBase } from 'vitepress'

import type { Tab } from '../../nav'

const props = defineProps<{ tab: Tab; open: boolean }>()

defineEmits<{ (e: 'close'): void }>()

const route = useRoute()

const current = computed(() => {
  const path = route.path.replace(/\.html$/, '').replace(/\/$/, '')

  return path + (route.hash ?? '')
})

const activeLink = computed(() => {
  const items = props.tab.groups.flatMap((group) => group.items)
  const exact = items.find((item) => item.link === current.value)

  if (exact) {
    return exact.link
  }

  const path = current.value.split('#')[0]

  return items.find((item) => item.link.split('#')[0] === path)?.link ?? ''
})
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
  </aside>
</template>
