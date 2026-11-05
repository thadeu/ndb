<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { withBase } from 'vitepress'

import { data as entries } from '../search.data'
import Icon from './Icon.vue'

const query = ref('')
const focused = ref(false)
const input = ref<HTMLInputElement | null>(null)

const results = computed(() => {
  const words = query.value.toLowerCase().split(/\s+/).filter(Boolean)

  if (words.length === 0) {
    return []
  }

  return entries
    .map((entry) => ({ entry, score: score(entry.haystack, entry.heading, words) }))
    .filter((hit) => hit.score > 0)
    .sort((a, b) => b.score - a.score)
    .slice(0, 8)
    .map((hit) => hit.entry)
})

function score(haystack: string, heading: string, words: string[]): number {
  let total = 0

  for (const word of words) {
    if (!haystack.includes(word)) {
      return 0
    }

    total += heading.toLowerCase().includes(word) ? 5 : 1
  }

  return total
}

function onKey(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  const typing = ['INPUT', 'TEXTAREA'].includes(target.tagName)

  if (event.key === '/' && !typing) {
    event.preventDefault()
    input.value?.focus()
  }

  if (event.key === 'Escape') {
    input.value?.blur()
    query.value = ''
  }
}

function hide() {
  window.setTimeout(() => {
    focused.value = false
  }, 120)
}

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="search">
    <Icon class="search-icon" name="search" :size="16" />

    <input
      ref="input"
      v-model="query"
      class="search-input"
      type="search"
      placeholder="Search"
      aria-label="Search the docs"
      @focus="focused = true"
      @blur="hide"
    />

    <kbd class="search-key">/</kbd>

    <ul v-if="focused && query" class="search-results">
      <li v-for="hit in results" :key="hit.url">
        <a :href="withBase(hit.url)">
          <strong>{{ hit.page }}</strong>
          <span v-if="hit.heading !== hit.page"> / {{ hit.heading }}</span>
          <small>{{ hit.excerpt }}</small>
        </a>
      </li>

      <li v-if="results.length === 0" class="search-empty">No results for "{{ query }}"</li>
    </ul>
  </div>
</template>
