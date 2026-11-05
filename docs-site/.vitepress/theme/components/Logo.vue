<script setup lang="ts">
import { withBase } from 'vitepress'

import markLight from '../../../../brand/svg/mark-animated-on-light.svg'
import markDark from '../../../../brand/svg/mark-animated-on-dark.svg'
</script>

<template>
  <a class="logo" :href="withBase('/')" aria-label="ndb home">
    <img class="only-light" :src="markLight" alt="ndb" height="44" />
    <img class="only-dark" :src="markDark" alt="ndb" height="44" />
  </a>
</template>
