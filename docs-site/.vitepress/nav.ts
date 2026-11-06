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
          { text: 'By _id', link: '/query#by-id' },
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
        text: 'Start',
        items: [{ text: 'Overview', link: '/clients' }],
      },
      {
        text: 'SDKs',
        items: [
          { text: 'Go', link: '/clients/go' },
          { text: 'Ruby', link: '/clients/ruby' },
          { text: 'Rails', link: '/clients/rails' },
          { text: 'TypeScript', link: '/clients/typescript' },
        ],
      },
      {
        text: 'Terminal',
        items: [{ text: 'CLI', link: '/clients/cli' }],
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
        text: 'Runbook',
        items: [
          { text: 'Overview', link: '/operations' },
          { text: 'Health and metrics', link: '/operations#check' },
          { text: 'Backup and restore', link: '/operations#backup-and-restore' },
          { text: 'Recovery', link: '/operations#recovery-after-a-crash' },
          { text: 'Compaction and TTL', link: '/operations#compaction-retention-and-ttl' },
          { text: 'Upgrade', link: '/operations#upgrade' },
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
        text: 'Start',
        items: [{ text: 'Choose a setup', link: '/deployment' }],
      },
      {
        text: 'Run ndb',
        items: [
          { text: 'Linux (systemd)', link: '/deploy/linux' },
          { text: 'Docker', link: '/deploy/docker' },
          { text: 'Voodu', link: '/deploy/voodu' },
        ],
      },
      {
        text: 'Topologies',
        items: [
          { text: 'Single VM', link: '/deploy/single-vm' },
          { text: 'Cross-VM (WireGuard)', link: '/deploy/cross-vm' },
        ],
      },
      {
        text: 'Contribute',
        items: [
          { text: 'Development', link: '/development' },
          { text: 'Releasing', link: '/releasing' },
        ],
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
  '/clients/go': 'clients',
  '/clients/ruby': 'clients',
  '/clients/rails': 'clients',
  '/clients/typescript': 'clients',
  '/clients/cli': 'clients',
  '/examples': 'examples',
  '/examples/crud': 'examples',
  '/examples/logs': 'examples',
  '/examples/reports': 'examples',
  '/examples/outbox': 'examples',
  '/examples/ttl-sessions': 'examples',
  '/operations': 'operations',
  '/deployment': 'deploy',
  '/deploy/linux': 'deploy',
  '/deploy/docker': 'deploy',
  '/deploy/voodu': 'deploy',
  '/deploy/single-vm': 'deploy',
  '/deploy/cross-vm': 'deploy',
  '/releasing': 'deploy',
  '/development': 'deploy',
}
