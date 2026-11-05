<script setup lang="ts">
import { withBase } from 'vitepress'

import lockupLight from '../../../../brand/svg/lockup-on-light.svg'
import lockupDark from '../../../../brand/svg/lockup-on-dark.svg'
</script>

<template>
  <a class="logo" :href="withBase('/')" aria-label="ndb home">
    <img class="only-light" :src="lockupLight" alt="ndb" height="50" />
    <img class="only-dark" :src="lockupDark" alt="ndb" height="50" />
  </a>
</template>
