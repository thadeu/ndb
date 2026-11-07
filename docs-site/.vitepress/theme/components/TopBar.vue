<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useData, withBase } from 'vitepress'

import Icon from './Icon.vue'
import Logo from './Logo.vue'
import Search from './Search.vue'

defineProps<{ drawerOpen: boolean }>()
defineEmits<{ (e: 'toggle-drawer'): void }>()

const repo = 'https://github.com/thadeu/ndb'
const { theme } = useData()
const dark = ref(false)

function readDark(): boolean {
  const forced = document.documentElement.getAttribute('data-theme')

  if (forced) {
    return forced === 'dark'
  }

  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function toggleTheme() {
  const next = dark.value ? 'light' : 'dark'

  document.documentElement.setAttribute('data-theme', next)
  dark.value = next === 'dark'

  try {
    localStorage.setItem('ndb-theme', next)
  } catch (error) {
    console.warn('theme not saved', error)
  }
}

onMounted(() => {
  dark.value = readDark()
})
</script>

<template>
  <header class="topbar">
    <Logo />

    <nav class="main-links" aria-label="Main">
      <a :href="withBase('/architecture')">Docs</a>
      <a :href="withBase('/clients')">Guides</a>
      <a :href="withBase('/http')">Reference</a>
      <a :href="withBase('/ui')">UI</a>
      <a class="icon-link" :href="repo" target="_blank" rel="noopener" aria-label="GitHub">
        <Icon name="github" :size="20" />
      </a>

      <a
        v-if="theme.version"
        class="version-pill"
        :href="`${repo}/blob/main/CHANGELOG.md`"
        target="_blank"
        rel="noopener"
        :title="`Docs for ndb ${theme.version}`"
      >
        v{{ theme.version }}
      </a>
    </nav>

    <div class="topbar-spacer" />

    <Search />

    <button class="icon-button" type="button" aria-label="Toggle dark mode" @click="toggleTheme">
      <Icon :name="dark ? 'sun' : 'moon'" :size="18" />
    </button>

    <a class="install-button" :href="`${repo}/releases`" target="_blank" rel="noopener">Install</a>

    <button class="icon-button menu-button" type="button" aria-label="Open the menu" @click="$emit('toggle-drawer')">
      <Icon :name="drawerOpen ? 'x' : 'menu'" :size="20" />
    </button>
  </header>
</template>
