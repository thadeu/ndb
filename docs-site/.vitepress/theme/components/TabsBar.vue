<script setup lang="ts">
import { withBase } from 'vitepress'

import { tabs } from '../../nav'
import Icon from './Icon.vue'

defineProps<{ activeId: string | null }>()
</script>

<template>
  <nav class="tabs" aria-label="Sections">
    <div class="tabs-scroll">
      <a
        v-for="tab in tabs"
        :key="tab.id"
        class="tab"
        :class="{ active: tab.id === activeId }"
        :href="withBase(tab.link)"
      >
        <Icon :name="tab.icon" :size="16" />
        <span>{{ tab.text }}</span>
      </a>
    </div>
  </nav>
</template>
