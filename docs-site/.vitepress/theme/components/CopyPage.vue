<script setup lang="ts">
import { ref } from 'vue'
import { useData } from 'vitepress'

import Icon from './Icon.vue'

const { frontmatter } = useData()
const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(frontmatter.value.raw ?? '')
    copied.value = true

    window.setTimeout(() => {
      copied.value = false
    }, 1600)
  } catch (error) {
    console.warn('copy failed', error)
  }
}
</script>

<template>
  <button class="copy-page" type="button" @click="copy">
    <Icon :name="copied ? 'check' : 'copy'" :size="15" />
    <span>{{ copied ? 'Copied' : 'Copy page' }}</span>
  </button>
</template>
