<script setup lang="ts">
import { withBase } from 'vitepress'

import wordDark from '../../../../brand/svg/wordmark-on-dark.svg'
import wordLight from '../../../../brand/svg/wordmark-on-light.svg'
import markDark from '../../../../brand/svg/mark-animated-on-dark.svg'
import markLight from '../../../../brand/svg/mark-animated-on-light.svg'
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
        text: 'NoSQL find with operators, sort, and cursor pagination.',
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

interface Step {
  title: string
  text: string
  code: string
}

const steps: Step[] = [
  {
    title: 'Point ndb at your files',
    text: 'Declare a collection in ndb.yml. ndb only reads the files.',
    code: `collections:
  events:
    write: direct
    path: /data/logs/*.ndjson
    time: ts`,
  },
  {
    title: 'Keep appending lines',
    text: 'Your app writes one JSON line per event. Any language works.',
    code: `echo '{"_id":"4","status":"answered","duration":10,...}' \\
  >> /data/logs/app.ndjson`,
  },
  {
    title: 'Query and aggregate',
    text: 'The new line is visible right away, with filters, sort and groups.',
    code: `# find where status answered, sort ts -1
{"_id":"4",...,"status":"answered","duration":10}

# aggregate: $group by status, $sum and $avg
{"_id":"answered","avg":49,"calls":3}
{"_id":"missed","avg":0,"calls":1}`,
  },
]

const capabilities = [
  'Filters and regex',
  'Sort and cursor pagination',
  'Aggregations',
  'Watch new lines',
  'Indexes',
  'Go, Ruby and TypeScript SDKs',
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
          Drop NDJSON. Query it. NoSQL, from one Go daemon, with Go, Ruby, and TypeScript SDKs that answer like <code>curl</code>.
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

    <section class="quick">
      <h2 class="quick-title">Drop an NDJSON file. Query it.</h2>

      <p class="quick-lead">No schema, no import, no migration.</p>

      <ol class="steps">
        <li v-for="(step, index) in steps" :key="step.title" class="step">
          <span class="step-number">{{ index + 1 }}</span>

          <h3 class="step-title">{{ step.title }}</h3>

          <p class="step-text">{{ step.text }}</p>

          <pre class="step-code"><code>{{ step.code }}</code></pre>
        </li>
      </ol>

      <ul class="chips">
        <li v-for="item in capabilities" :key="item" class="chip">{{ item }}</li>
      </ul>
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
