<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Content, useData, useRoute, withBase } from 'vitepress'

import bowl from '../../../brand/svg/mark-on-light.svg'
import { pageTab, tabs } from '../nav'
import CopyPage from './components/CopyPage.vue'
import Landing from './components/Landing.vue'
import Outline from './components/Outline.vue'
import Sidebar from './components/Sidebar.vue'
import TabsBar from './components/TabsBar.vue'
import TopBar from './components/TopBar.vue'

const route = useRoute()
const { page, frontmatter } = useData()

const drawerOpen = ref(false)

const path = computed(() => route.path.replace(/\.html$/, '').replace(/\/$/, '') || '/')
const isHome = computed(() => frontmatter.value.layout === 'home')

const activeId = computed(() => {
  if (path.value === '/query' && (route.hash ?? '').startsWith('#aggregate')) {
    return 'aggregate'
  }

  if (path.value === '/http-api' && (route.hash ?? '').startsWith('#watch')) {
    return 'watch'
  }

  return pageTab[path.value] ?? null
})

const activeTab = computed(() => tabs.find((tab) => tab.id === activeId.value) ?? tabs[0])

function copyCode(event: MouseEvent) {
  const button = (event.target as HTMLElement).closest('button.copy')
  const code = button?.parentElement?.querySelector('pre code')

  if (!button || !code) {
    return
  }

  navigator.clipboard.writeText(code.textContent ?? '').then(() => {
    button.classList.add('copied')
    window.setTimeout(() => button.classList.remove('copied'), 1600)
  })
}

onMounted(() => {
  document.addEventListener('click', copyCode)
})

watch(
  () => route.path,
  () => {
    drawerOpen.value = false
  },
)
</script>

<template>
  <div class="app" :class="{ 'drawer-open': drawerOpen }">
    <TopBar :drawer-open="drawerOpen" @toggle-drawer="drawerOpen = !drawerOpen" />

    <TabsBar :active-id="isHome ? null : activeId" />

    <Landing v-if="isHome" />

    <main v-else-if="page.isNotFound" class="landing">
      <img class="lost-art" :src="bowl" alt="The ndb ramen bowl" />

      <p class="eyebrow">404</p>

      <h1 class="hero-title">Page not found</h1>

      <p class="hero-lead">This page does not exist. Go back to the docs.</p>

      <a class="install-button large" :href="withBase('/')">Home</a>
    </main>

    <div v-else class="shell">
      <Sidebar :tab="activeTab" :open="drawerOpen" @close="drawerOpen = false" />

      <div class="scrim" @click="drawerOpen = false" />

      <main class="main">
        <article class="doc">
          <div class="doc-head">
            <nav class="crumbs" aria-label="Breadcrumb">
              <span>Docs</span>
              <span class="sep">/</span>
              <span>{{ activeTab.text }}</span>
              <span class="sep">/</span>
              <span class="current">{{ page.title }}</span>
            </nav>

            <CopyPage />
          </div>

          <Content class="doc-content" />
        </article>
      </main>

      <Outline />
    </div>
  </div>
</template>
