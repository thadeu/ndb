export interface SidebarItem {
  text: string
  link: string
}

export interface SidebarGroup {
  text: string
  items: SidebarItem[]
}

export interface Tab {
  id: string
  text: string
  icon: string
  link: string
  groups: SidebarGroup[]
}

export const tabs: Tab[] = [
  {
    id: 'daemon',
    text: 'Daemon',
    icon: 'server',
    link: '/architecture',
    groups: [
      {
        text: 'Overview',
        items: [
          { text: 'Architecture', link: '/architecture' },
          { text: 'Storage', link: '/storage' },
          { text: 'WAL', link: '/wal' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Configuration', link: '/configuration' },
          { text: 'HTTP API', link: '/http-api' },
          { text: 'Errors', link: '/http-api#errors' },
        ],
      },
    ],
  },
  {
    id: 'query',
    text: 'Query',
    icon: 'search',
    link: '/query',
    groups: [
      {
        text: 'Read',
        items: [
          { text: 'find', link: '/query#find' },
          { text: 'Filters', link: '/query#filters' },
          { text: 'Regular expressions', link: '/query#regular-expressions' },
          { text: 'Pagination', link: '/query#pagination' },
        ],
      },
      {
        text: 'Write',
        items: [
          { text: 'Update by filter', link: '/query#update-by-filter' },
          { text: 'Write operators', link: '/query#write-operators' },
          { text: 'Arrays', link: '/query#arrays' },
          { text: 'Upsert', link: '/query#upsert' },
          { text: 'Delete by filter', link: '/query#delete-by-filter' },
        ],
      },
    ],
  },
  {
    id: 'aggregate',
    text: 'Aggregate',
    icon: 'chart',
    link: '/aggregate',
    groups: [
      {
        text: 'Aggregate',
        items: [
          { text: 'Overview', link: '/aggregate' },
          { text: 'Pipeline shape', link: '/aggregate#pipeline-shape' },
          { text: 'Group key', link: '/aggregate#group-key' },
          { text: 'Accumulators', link: '/aggregate#accumulators' },
          { text: 'Time buckets', link: '/aggregate#time-buckets' },
          { text: 'Limits', link: '/aggregate#limits' },
        ],
      },
    ],
  },
  {
    id: 'watch',
    text: 'Watch',
    icon: 'bell',
    link: '/watch',
    groups: [
      {
        text: 'Watch',
        items: [
          { text: 'Overview', link: '/watch' },
          { text: 'Request', link: '/watch#request' },
          { text: 'Events', link: '/watch#events' },
          { text: 'Ack and delivery', link: '/watch#ack-and-delivery' },
          { text: 'Errors', link: '/watch#errors' },
        ],
      },
    ],
  },
  {
    id: 'clients',
    text: 'Clients',
    icon: 'plug',
    link: '/clients',
    groups: [
      {
        text: 'SDKs',
        items: [
          { text: 'Ruby', link: '/clients#ruby-gem-ndb' },
          { text: 'Rails', link: '/clients#rails-gem-ndb-record' },
          { text: 'Go', link: '/clients#go-module-github-com-thadeu-ndb-sdk-go' },
          { text: 'TypeScript', link: '/clients#typescript-package-ndb' },
          { text: 'Direct mode', link: '/clients#direct-mode-from-an-app' },
        ],
      },
    ],
  },
  {
    id: 'examples',
    text: 'Examples',
    icon: 'code',
    link: '/examples',
    groups: [
      {
        text: 'Start',
        items: [{ text: 'Overview', link: '/examples' }],
      },
      {
        text: 'Examples',
        items: [
          { text: 'CRUD', link: '/examples/crud' },
          { text: 'Logs (direct mode)', link: '/examples/logs' },
          { text: 'Reports (aggregate)', link: '/examples/reports' },
          { text: 'Outbox (watch and ack)', link: '/examples/outbox' },
          { text: 'Expiring sessions', link: '/examples/ttl-sessions' },
        ],
      },
    ],
  },
  {
    id: 'operations',
    text: 'Operations',
    icon: 'gauge',
    link: '/operations',
    groups: [
      {
        text: 'Run',
        items: [
          { text: 'Runbook', link: '/operations' },
          { text: 'Docker', link: '/docker' },
        ],
      },
    ],
  },
  {
    id: 'deploy',
    text: 'Deploy',
    icon: 'rocket',
    link: '/deployment',
    groups: [
      {
        text: 'Deploy',
        items: [
          { text: 'Deployment', link: '/deployment' },
          { text: 'Docker', link: '/docker' },
        ],
      },
      {
        text: 'Contribute',
        items: [{ text: 'Development', link: '/development' }],
      },
    ],
  },
]

export const pageTab: Record<string, string> = {
  '/architecture': 'daemon',
  '/storage': 'daemon',
  '/wal': 'daemon',
  '/configuration': 'daemon',
  '/http-api': 'daemon',
  '/query': 'query',
  '/aggregate': 'aggregate',
  '/watch': 'watch',
  '/clients': 'clients',
  '/examples': 'examples',
  '/examples/crud': 'examples',
  '/examples/logs': 'examples',
  '/examples/reports': 'examples',
  '/examples/outbox': 'examples',
  '/examples/ttl-sessions': 'examples',
  '/operations': 'operations',
  '/docker': 'operations',
  '/deployment': 'deploy',
  '/development': 'deploy',
}
