<script setup lang="ts">
import { computed } from 'vue'
import { useData } from 'vitepress'

const { page } = useData()

const headers = computed(() =>
  (page.value.headers ?? []).flatMap((h) => [h, ...(h.children ?? [])]).filter((h) => h.level <= 3),
)
</script>

<template>
  <aside v-if="headers.length > 1" class="outline" aria-label="On this page">
    <h4 class="outline-title">On this page</h4>

    <a
      v-for="header in headers"
      :key="header.link"
      class="outline-item"
      :class="{ nested: header.level === 3 }"
      :href="header.link"
    >
      {{ header.title }}
    </a>
  </aside>
</template>
