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
          { text: 'HTTP API', link: '/http' },
          { text: 'Errors', link: '/http/errors' },
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
          { text: 'Search', link: '/query#search' },
          { text: 'Filters', link: '/query#filters' },
          { text: 'Regular expressions', link: '/query#regular-expressions' },
          { text: 'Date math', link: '/query#date-math' },
          { text: 'Array elements', link: '/query#array-elements' },
          { text: 'Collection patterns', link: '/http/queries#collection-patterns' },
          { text: 'Pagination', link: '/query#pagination' },
        ],
      },
      {
        text: 'Write',
        items: [
          { text: 'By _id', link: '/query#by-id' },
          { text: 'Update by query', link: '/query#update-by-query' },
          { text: 'Write operators', link: '/query#write-operators' },
          { text: 'Arrays', link: '/query#arrays' },
          { text: 'Upsert', link: '/query#upsert' },
          { text: 'Delete by query', link: '/query#delete-by-query' },
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
      {
        text: 'Streams',
        items: [
          { text: 'Overview', link: '/streams' },
          { text: 'A worker', link: '/streams#a-worker' },
          { text: 'How the engine runs it', link: '/streams#how-the-engine-runs-it' },
          { text: 'Trim and retention', link: '/streams#trim-and-retention' },
          { text: 'Redis command map', link: '/streams#redis-command-map' },
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
          { text: 'Point in time recovery', link: '/pitr' },
          { text: 'Recovery', link: '/operations#recovery-after-a-crash' },
          { text: 'Compaction and TTL', link: '/operations#compaction-retention-and-ttl' },
          { text: 'Upgrade', link: '/operations#upgrade' },
        ],
      },
    ],
  },
  {
    id: 'http',
    text: 'HTTP',
    icon: 'globe',
    link: '/http',
    groups: [
      {
        text: 'Start',
        items: [
          { text: 'Overview', link: '/http' },
          { text: 'Errors', link: '/http/errors' },
        ],
      },
      {
        text: 'Routes',
        items: [
          { text: 'System', link: '/http/system' },
          { text: 'Collections', link: '/http/collections' },
          { text: 'Documents', link: '/http/documents' },
          { text: 'Queries', link: '/http/queries' },
          { text: 'Watch', link: '/http/watch' },
          { text: 'Streams', link: '/http/streams' },
        ],
      },
    ],
  },
  {
    id: 'ui',
    text: 'UI',
    icon: 'monitor',
    link: '/ui',
    groups: [
      {
        text: 'Start',
        items: [
          { text: 'Overview', link: '/ui' },
          { text: 'Install', link: '/ui/install' },
          { text: 'Docker', link: '/ui/docker' },
        ],
      },
      {
        text: 'Guide',
        items: [
          { text: 'Connections', link: '/ui/guide#connections' },
          { text: 'Overview', link: '/ui/guide#overview' },
          { text: 'Browser', link: '/ui/guide#browser' },
          { text: 'Dev Tools', link: '/ui/guide#dev-tools' },
          { text: 'Watch', link: '/ui/guide#watch' },
          { text: 'Backups', link: '/ui/guide#backups' },
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
  '/http': 'http',
  '/http/system': 'http',
  '/http/collections': 'http',
  '/http/documents': 'http',
  '/http/queries': 'http',
  '/http/watch': 'http',
  '/http/streams': 'http',
  '/http/errors': 'http',
  '/query': 'query',
  '/aggregate': 'aggregate',
  '/watch': 'watch',
  '/streams': 'watch',
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
  '/ui': 'ui',
  '/ui/install': 'ui',
  '/ui/docker': 'ui',
  '/ui/guide': 'ui',
  '/releasing': 'deploy',
  '/development': 'deploy',
}
