<script setup lang="ts">
import { withBase } from 'vitepress'

import wordDark from '../../../../brand/svg/wordmark-on-dark.svg'
import wordLight from '../../../../brand/svg/wordmark-on-light.svg'
import markDark from '../../../../brand/svg/mark-on-dark.svg'
import markLight from '../../../../brand/svg/mark-on-light.svg'
import Icon from './Icon.vue'

interface Card {
  icon: string
  title: string
  text: string
  command: string
  link: string
}

interface Section {
  title: string
  cards: Card[]
}

const sections: Section[] = [
  {
    title: 'Build with ndb',
    cards: [
      {
        icon: 'search',
        title: 'Query',
        text: 'Mongo-style find with operators, sort, and cursor pagination.',
        command: 'POST /v1/c/calls/find',
        link: '/query',
      },
      {
        icon: 'chart',
        title: 'Aggregate',
        text: 'Group, match, and bucket by time with $dateTrunc.',
        command: 'POST /v1/c/calls/aggregate',
        link: '/query#aggregate',
      },
      {
        icon: 'bell',
        title: 'Watch',
        text: 'Outbox consumers with ack, a where filter, and wal_retention.',
        command: 'POST /v1/c/calls/watch',
        link: '/http-api#watch-and-ack',
      },
      {
        icon: 'plug',
        title: 'Clients',
        text: 'Go, Ruby, Rails, and TypeScript SDKs over a unix socket or TCP with a token.',
        command: 'gem "ndb-rails"',
        link: '/clients',
      },
    ],
  },
  {
    title: 'Run ndb',
    cards: [
      {
        icon: 'server',
        title: 'Daemon',
        text: 'One static Go binary, one data directory, one config file.',
        command: 'ndb -config ndb.yml',
        link: '/configuration',
      },
      {
        icon: 'database',
        title: 'Storage',
        text: 'NDJSON is the source of truth. Indexes and segments are derived.',
        command: 'ndb -data-dir /var/lib/ndb',
        link: '/storage',
      },
      {
        icon: 'gauge',
        title: 'Operations',
        text: 'Backup, metrics, quarantine, recovery, and compaction.',
        command: 'ndb backup /backups/now',
        link: '/operations',
      },
      {
        icon: 'rocket',
        title: 'Deploy',
        text: 'One VM, or many VMs over WireGuard with a bearer token.',
        command: 'ndb -token-file /etc/ndb/token',
        link: '/deployment',
      },
    ],
  },
]
</script>

<template>
  <main class="landing">
    <section class="hero">
      <div class="hero-text">
        <p class="eyebrow">Documentation</p>

        <h1 class="hero-title">
          <span class="sr-only">ndb</span>
          <img class="only-light" :src="wordLight" alt="" aria-hidden="true" />
          <img class="only-dark" :src="wordDark" alt="" aria-hidden="true" />
        </h1>

        <p class="hero-lead">
          A query engine for NDJSON. One Go daemon with a MongoDB-style JSON API over a unix socket or TCP.
          Go, Ruby, and TypeScript SDKs give the same answer as <code>curl</code>.
        </p>

        <div class="hero-actions">
          <a class="install-button large" :href="withBase('/query')">Get started</a>
          <a class="ghost-button" :href="withBase('/docker')">Run with Docker</a>
        </div>
      </div>

      <div class="hero-art">
        <img class="only-light" :src="markLight" alt="The ndb ramen bowl mascot" />
        <img class="only-dark" :src="markDark" alt="The ndb ramen bowl mascot" />
      </div>
    </section>

    <section v-for="section in sections" :key="section.title" class="card-section">
      <h2 class="card-section-title">{{ section.title }}</h2>

      <div class="cards">
        <a v-for="card in section.cards" :key="card.title" class="card" :href="withBase(card.link)">
          <Icon class="card-icon" :name="card.icon" :size="22" />

          <h3 class="card-title">{{ card.title }}</h3>

          <p class="card-text">{{ card.text }}</p>

          <span class="card-link">
            Get started with <code>{{ card.command }}</code>
            <Icon name="arrow" :size="14" />
          </span>
        </a>
      </div>
    </section>
  </main>
</template>
