<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'

interface CallEvent {
  id: string
  ts: string
  status: string
  duration: number
}

interface Stat {
  calls: number
  total: number
}

const STATUSES = ['answered', 'missed', 'busy']
const VISIBLE = 5
const TICK_MS = 1600

const sequence: Array<[string, number, number]> = [
  ['answered', 42, 3],
  ['missed', 0, 2],
  ['answered', 87, 5],
  ['busy', 0, 2],
  ['answered', 15, 4],
  ['answered', 63, 3],
  ['missed', 0, 6],
  ['answered', 118, 2],
  ['busy', 0, 3],
  ['answered', 29, 4],
]

let cursor = 0
let clock = 10 * 3600 + 41 * 60 + 38
let timer: ReturnType<typeof setInterval> | undefined
const tweens: Record<string, number> = {}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

function nextEvent(): CallEvent {
  const [status, duration, gap] = sequence[cursor % sequence.length]

  clock += gap
  cursor += 1

  const hours = Math.floor(clock / 3600) % 24
  const minutes = Math.floor((clock % 3600) / 60)
  const seconds = clock % 60

  return {
    id: `c${100 + cursor}`,
    ts: `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`,
    status,
    duration,
  }
}

function emptyStats(): Record<string, Stat> {
  return Object.fromEntries(STATUSES.map((status) => [status, { calls: 0, total: 0 }]))
}

const lines = ref<CallEvent[]>([])
const stats = emptyStats()
const shown = reactive<Record<string, { calls: number; avg: number }>>(
  Object.fromEntries(STATUSES.map((status) => [status, { calls: 0, avg: 0 }])),
)
const flashing = reactive<Record<string, boolean>>({})
const newestId = ref('')

function record(event: CallEvent) {
  stats[event.status].calls += 1
  stats[event.status].total += event.duration
}

function target(status: string) {
  const { calls, total } = stats[status]

  return { calls, avg: calls ? Math.round(total / calls) : 0 }
}

for (let index = 0; index < 6; index += 1) {
  const event = nextEvent()

  lines.value.push(event)
  record(event)
}

lines.value = lines.value.slice(-VISIBLE)
newestId.value = ''

for (const status of STATUSES) {
  Object.assign(shown[status], target(status))
}

const rows = computed(() => STATUSES.filter((status) => stats[status].calls > 0 || shown[status].calls > 0).sort())

const command = `$ ndb query calls -aggregate '[{"$group":{"_id":"$status","calls":{"$sum":1},"avg":{"$avg":"$duration"}}}]'`

function lineText(event: CallEvent): string {
  return JSON.stringify({ _id: event.id, ts: event.ts, status: event.status, duration: event.duration })
}

function tween(status: string) {
  const from = { ...shown[status] }
  const to = target(status)
  const started = performance.now()
  const length = 450

  cancelAnimationFrame(tweens[status])
  flashing[status] = true
  window.setTimeout(() => {
    flashing[status] = false
  }, 900)

  function frame(now: number) {
    const progress = Math.min(1, Math.max(0, (now - started) / length))

    shown[status].calls = Math.round(from.calls + (to.calls - from.calls) * progress)
    shown[status].avg = Math.round(from.avg + (to.avg - from.avg) * progress)

    if (progress < 1) {
      tweens[status] = requestAnimationFrame(frame)
    }
  }

  tweens[status] = requestAnimationFrame(frame)
}

function tick() {
  const event = nextEvent()

  lines.value = [...lines.value, event].slice(-VISIBLE)
  newestId.value = event.id
  record(event)
  tween(event.status)
}

function start() {
  if (timer === undefined) {
    timer = setInterval(tick, TICK_MS)
  }
}

function stop() {
  if (timer !== undefined) {
    clearInterval(timer)
    timer = undefined
  }
}

function onVisibility() {
  if (document.hidden) {
    stop()
  } else {
    start()
  }
}

onMounted(() => {
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    return
  }

  document.addEventListener('visibilitychange', onVisibility)

  if (!document.hidden) {
    start()
  }
})

onBeforeUnmount(() => {
  stop()
  document.removeEventListener('visibilitychange', onVisibility)
  Object.values(tweens).forEach((handle) => cancelAnimationFrame(handle))
})
</script>

<template>
  <div class="demo" role="img" aria-label="Live demo: new NDJSON lines are appended to a log file and an aggregation over the file updates as they arrive.">
    <div class="demo-pane">
      <div class="demo-bar">
        <span class="demo-dots" aria-hidden="true"><i></i><i></i><i></i></span>

        <span class="demo-title">logs/app.ndjson</span>

        <span class="demo-tag">tail -f</span>
      </div>

      <ol class="demo-lines" aria-hidden="true">
        <li
          v-for="event in lines"
          :key="event.id"
          class="demo-line"
          :class="{ fresh: event.id === newestId }"
        >
          {{ lineText(event) }}
        </li>
      </ol>
    </div>

    <div class="demo-pane demo-result">
      <p class="demo-command" aria-hidden="true">{{ command }}</p>

      <table class="demo-table" aria-hidden="true">
        <thead>
          <tr>
            <th>status</th>
            <th class="num">calls</th>
            <th class="num">avg s</th>
          </tr>
        </thead>

        <tbody>
          <tr v-for="status in rows" :key="status" :class="{ hot: flashing[status] }">
            <td>{{ status }}</td>
            <td class="num">{{ shown[status].calls }}</td>
            <td class="num">{{ shown[status].avg }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
