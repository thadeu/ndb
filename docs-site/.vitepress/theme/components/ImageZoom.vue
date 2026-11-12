<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vitepress'

// ImageZoom opens a screenshot of the docs at full size in a modal. The site
// turns off pinch-zoom, so this is the only way to read a small shot.
const SELECTOR = '.doc-content .ui-shot img, .doc-content p > img[src*="/img/"]'

const route = useRoute()

const src = ref<string | null>(null)
const alt = ref('')
const zoomed = ref(false)

function open(event: MouseEvent) {
  const img = (event.target as HTMLElement).closest<HTMLImageElement>(SELECTOR)

  if (!img) {
    return
  }

  src.value = img.currentSrc || img.src
  alt.value = img.alt
  zoomed.value = false
  document.body.style.overflow = 'hidden'
}

function close() {
  src.value = null
  document.body.style.overflow = ''
}

// toggle shows the image at its full width on a phone, to pan and read it.
function toggle() {
  zoomed.value = !zoomed.value
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && src.value) {
    close()
  }
}

onMounted(() => {
  document.addEventListener('click', open)
  document.addEventListener('keydown', onKey)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', open)
  document.removeEventListener('keydown', onKey)
})

watch(() => route.path, close)
</script>

<template>
  <Transition name="zoom">
    <div
      v-if="src"
      class="image-zoom"
      :class="{ zoomed }"
      role="dialog"
      aria-modal="true"
      :aria-label="alt"
      @click="close"
    >
      <button class="image-zoom-close" type="button" aria-label="Close">×</button>

      <img :src="src" :alt="alt" @click.stop="toggle" />

      <p v-if="alt" class="image-zoom-caption">{{ alt }}</p>
    </div>
  </Transition>
</template>
