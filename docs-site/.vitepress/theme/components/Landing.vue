<script setup lang="ts">
import { withBase } from 'vitepress'

import { ref } from 'vue'

import HeroDemo from './HeroDemo.vue'
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
        text: 'Search with operators, date math, sort, and cursor pagination.',
        command: 'POST /calls/_search',
        link: '/query',
      },
      {
        icon: 'chart',
        title: 'Aggregate',
        text: 'Group, match, and bucket by time with $dateTrunc.',
        command: 'POST /calls/_aggregate',
        link: '/aggregate',
      },
      {
        icon: 'bell',
        title: 'Watch',
        text: 'Outbox consumers with ack, a query filter, and wal_retention.',
        command: 'POST /calls/_watch',
        link: '/watch',
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

interface UseCase {
  icon: string
  title: string
  text: string
  snippet: string
  link: string
}

const useCases: UseCase[] = [
  {
    icon: 'server',
    title: 'Logs and events',
    text: 'Your app already writes NDJSON. Query and aggregate it in place, no ELK, no import.',
    snippet: 'write: direct\npath: /var/log/app/*.ndjson',
    link: '/examples/logs',
  },
  {
    icon: 'bell',
    title: 'Outbox and queues',
    text: 'watch with ack and a query filter: a reliable consumer without Kafka or Redis.',
    snippet: 'POST /outbox/_watch\n{"consumer":"mailer","query":{"type":"email"}}',
    link: '/examples/outbox',
  },
  {
    icon: 'chart',
    title: 'Reports and dashboards',
    text: '$group and $dateTrunc answer "calls per hour" straight from the files.',
    snippet: '{"$group":{"_id":{"$dateTrunc":\n  {"date":"$ts","unit":"hour"}},\n  "n":{"$sum":1}}}',
    link: '/examples/reports',
  },
  {
    icon: 'database',
    title: 'Small apps',
    text: 'One binary and one data dir hold your app state. No Postgres to run.',
    snippet: 'class Post < Ndb::Record',
    link: '/examples/crud',
  },
  {
    icon: 'rocket',
    title: 'Scripts and CI',
    text: 'ndb query in the terminal, -json in a pipe, the same answer as the SDKs.',
    snippet: "ndb query calls '{\"status\":\"missed\"}' -json | jq",
    link: '/examples',
  },
]

const installCommand = 'curl -fsSL https://ndb.clowk.in/install.sh | bash'
const copied = ref(false)
const installBox = ref<HTMLElement | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | undefined

function markCopied() {
  copied.value = true
  clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copied.value = false
  }, 1500)
}

async function copyInstall() {
  try {
    await navigator.clipboard.writeText(installCommand)
    markCopied()
  } catch {
    const target = installBox.value

    if (target) {
      const range = document.createRange()

      range.selectNodeContents(target)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
    }
  }
}

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
    code: `# _search: query status answered, sort ts -1
[{"_id":"4",...,"status":"answered","duration":10}]

# _aggregate: $group by status, $sum and $avg
[{"_id":"answered","avg":49,"calls":3},
 {"_id":"missed","avg":0,"calls":1}]`,
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
        <p class="eyebrow">NoSQL on NDJSON</p>

        <h1 class="hero-title">
          <span class="sr-only">ndb: </span>
          Your NDJSON files are already a database.
        </h1>

        <p class="hero-lead">
          Drop NDJSON. Query it. NoSQL. Filters, aggregations and live watch over the files your app already writes, from one Go binary with Go, Ruby and TypeScript SDKs.
        </p>

        <div class="hero-actions">
          <div class="install-box">
            <code ref="installBox" class="install-command">{{ installCommand }}</code>

            <button
              class="install-copy"
              type="button"
              :aria-label="copied ? 'Copied' : 'Copy install command'"
              @click="copyInstall"
            >
              <Icon :name="copied ? 'check' : 'copy'" :size="15" />

              <span class="install-copy-label">{{ copied ? 'Copied' : 'Copy' }}</span>
            </button>
          </div>

          <div class="hero-buttons">
            <a class="install-button large" :href="withBase('/query')">Get started</a>
            <a class="ghost-button" href="#use-cases">Use cases</a>
          </div>
        </div>
      </div>

      <HeroDemo class="hero-demo" />
    </section>

    <section id="use-cases" class="use-cases">
      <h2 class="card-section-title">Use cases</h2>

      <p class="quick-lead">Where ndb earns its place.</p>

      <div class="use-grid">
        <a v-for="item in useCases" :key="item.title" class="use-card" :href="withBase(item.link)">
          <Icon class="card-icon" :name="item.icon" :size="22" />

          <h3 class="card-title">{{ item.title }}</h3>

          <p class="card-text">{{ item.text }}</p>

          <pre class="use-snippet"><code>{{ item.snippet }}</code></pre>
        </a>
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
