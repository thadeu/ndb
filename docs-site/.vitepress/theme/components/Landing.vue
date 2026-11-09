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
        text: 'MongoDB operators on Elasticsearch-style routes: filters, date math, sort, cursors.',
        command: 'POST /calls/_search',
        link: '/http/queries',
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
        text: 'Go, Ruby, Rails, and TypeScript SDKs with one vocabulary: query, search, upsert.',
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

const runSteps: Step[] = [
  {
    title: 'Start the daemon',
    text: 'No flags, no root, no config file: data in ~/.ndb, HTTP on 127.0.0.1:7070.',
    code: `ndb serve`,
  },
  {
    title: 'Write and search',
    text: 'The first write makes the collection. Ctrl+C stops the daemon.',
    code: `curl localhost:7070/calls/_doc \\
  -d '{"status":"missed"}'

curl localhost:7070/calls/_search \\
  -d '{"query":{"status":"missed"}}'`,
  },
]

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
  'Elasticsearch-style routes',
  'MongoDB operators',
  'Date math: now-7d/d',
  '$elemMatch',
  'Collection patterns: logs-*',
  'Upsert and atomic $inc',
  'Aggregations',
  'Watch and ack',
  'Go, Ruby, Rails and TypeScript SDKs',
]

interface Borrowed {
  what: string
  example: string
}

const fromElastic: Borrowed[] = [
  { what: 'Routes you already know', example: 'POST /logs/_search, _count, _bulk' },
  { what: 'Writes by query', example: '_update_by_query, _delete_by_query' },
  { what: 'Index patterns', example: 'POST /logs-2026-*/_search' },
  { what: 'Date math', example: '{"ts": {"$gte": "now-7d/d"}}' },
  { what: 'Upsert by id', example: '_bulk {"docs": [...], "upsert": true}' },
]

const fromMongo: Borrowed[] = [
  { what: 'A query is plain JSON', example: '{"status": {"$in": ["missed", "busy"]}}' },
  { what: 'Rich filters', example: '$gte $regex $exists $or $not $elemMatch' },
  { what: 'Atomic updates', example: '{"$inc": {"qty": -1}, "$push": {...}}' },
  { what: 'Pipelines', example: '$match $group $sort $dateTrunc' },
  { what: 'Optimistic locks', example: '{"query": {"_v": 7}, "update": {...}}' },
]

interface Snippet {
  label: string
  code: string
}

const snippets: Snippet[] = [
  {
    label: 'curl',
    code: `curl --unix-socket /run/ndb/ndb.sock http://ndb/calls/_search -d '{
  "query": {
    "status": { "$in": ["missed", "busy"] },
    "ts": { "$gte": "now-1d/d" }
  },
  "sort": { "ts": -1 },
  "limit": 50
}'`,
  },
  {
    label: 'Go',
    code: `rows, err := calls.
  Query(ndb.In("status", "missed", "busy"), ndb.Gte("ts", "now-1d/d")).
  Sort(ndb.Desc("ts")).
  Limit(50).
  All(ctx)`,
  },
  {
    label: 'Ruby',
    code: `calls.query(status: %w[missed busy], ts: { "$gte" => "now-1d/d" })
     .sort(ts: -1)
     .limit(50)
     .all`,
  },
  {
    label: 'Rails',
    code: `Call.where(status: %w[missed busy])
    .where(ts: { "$gte" => "now-1d/d" })
    .order(ts: :desc)
    .limit(50)`,
  },
  {
    label: 'TypeScript',
    code: `const rows = await calls
  .query({ status: { $in: ['missed', 'busy'] }, ts: { $gte: 'now-1d/d' } })
  .sort({ ts: -1 })
  .limit(50)
  .all()`,
  },
]

const activeSnippet = ref(0)

interface Reason {
  icon: string
  title: string
  text: string
}

const reasons: Reason[] = [
  {
    icon: 'server',
    title: 'No JVM, no cluster',
    text: 'One static Go binary and one data directory. It starts in a blink and runs next to your app.',
  },
  {
    icon: 'database',
    title: 'Your data stays plain NDJSON',
    text: 'The source of truth is files you can read with cat and jq. Indexes and segments are rebuilt from them.',
  },
  {
    icon: 'search',
    title: 'Filters you do not have to learn',
    text: 'If you wrote a MongoDB query, you can write an ndb query. Same operators, same JSON.',
  },
  {
    icon: 'check',
    title: 'Writes that do not race',
    text: 'Update and delete by query run in one atomic step: a guarded $inc never sells the last item twice.',
  },
  {
    icon: 'bell',
    title: 'Changes as a stream',
    text: 'watch and ack give you an outbox consumer without Kafka or Redis.',
  },
  {
    icon: 'globe',
    title: 'A contract, not a guess',
    text: 'Every route, header, limit and error code is written down. curl and four SDKs speak the same API.',
  },
]
</script>

<template>
  <main class="landing">
    <section class="hero">
      <div class="hero-text">
        <p class="eyebrow">Elasticsearch API · MongoDB queries · one Go binary</p>

        <h1 class="hero-title">
          <span class="sr-only">ndb: </span>
          The API of Elasticsearch. The filters of MongoDB.
        </h1>

        <p class="hero-lead">
          <code>POST /logs/_search</code> with a <code>{"$gte": "now-7d"}</code> query. ndb takes the routes developers know from Elasticsearch and the query language they know from MongoDB, and runs them on plain NDJSON from one small daemon. No JVM, no cluster, no schema.
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
            <a class="install-button large" :href="withBase('/http')">Read the API</a>
            <a class="ghost-button" href="#try">Run it</a>
          </div>
        </div>
      </div>

      <HeroDemo class="hero-demo" />
    </section>

    <section id="try" class="quick">
      <h2 class="quick-title">Run it in one minute</h2>

      <p class="quick-lead">Start a daemon, write a document. Run <code>ndb help</code> for every command.</p>

      <ol class="steps steps-two">
        <li v-for="(step, index) in runSteps" :key="step.title" class="step">
          <span class="step-number">{{ index + 1 }}</span>

          <h3 class="step-title">{{ step.title }}</h3>

          <p class="step-text">{{ step.text }}</p>

          <pre class="step-code"><code>{{ step.code }}</code></pre>
        </li>
      </ol>
    </section>

    <section id="best-of-both" class="both">
      <h2 class="card-section-title">The best of two worlds</h2>

      <p class="quick-lead">The URL reads like Elasticsearch. The body reads like MongoDB. Both are what you already know.</p>

      <div class="both-grid">
        <div class="both-column">
          <h3 class="both-title">From Elasticsearch</h3>

          <ul class="both-list">
            <li v-for="item in fromElastic" :key="item.what">
              <span class="both-what">{{ item.what }}</span>
              <code class="both-example">{{ item.example }}</code>
            </li>
          </ul>
        </div>

        <div class="both-column">
          <h3 class="both-title">From MongoDB</h3>

          <ul class="both-list">
            <li v-for="item in fromMongo" :key="item.what">
              <span class="both-what">{{ item.what }}</span>
              <code class="both-example">{{ item.example }}</code>
            </li>
          </ul>
        </div>
      </div>

      <div class="snippet-card">
        <div class="snippet-tabs" role="tablist" aria-label="The same search in every client">
          <button
            v-for="(item, index) in snippets"
            :key="item.label"
            class="snippet-tab"
            :class="{ active: index === activeSnippet }"
            type="button"
            role="tab"
            :aria-selected="index === activeSnippet"
            @click="activeSnippet = index"
          >
            {{ item.label }}
          </button>
        </div>

        <pre class="snippet-code"><code>{{ snippets[activeSnippet].code }}</code></pre>

        <p class="snippet-note">One search: calls missed or busy since the start of yesterday, newest first. The same query from curl and every SDK.</p>
      </div>
    </section>

    <section class="reasons">
      <h2 class="card-section-title">Why developers try ndb</h2>

      <div class="use-grid">
        <div v-for="item in reasons" :key="item.title" class="use-card reason">
          <Icon class="card-icon" :name="item.icon" :size="22" />

          <h3 class="card-title">{{ item.title }}</h3>

          <p class="card-text">{{ item.text }}</p>
        </div>
      </div>

      <p class="honest-note">ndb filters, sorts and aggregates. It does not rank full-text results like a search engine; for that, keep Elasticsearch.</p>
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
