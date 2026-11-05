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
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Configuration', link: '/configuration' },
          { text: 'HTTP API', link: '/http-api' },
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
        text: 'Query language',
        items: [
          { text: 'find', link: '/query#find' },
          { text: 'Operators', link: '/query#operators' },
          { text: 'Pagination', link: '/query#pagination' },
          { text: 'Errors', link: '/query#errors' },
        ],
      },
    ],
  },
  {
    id: 'aggregate',
    text: 'Aggregate',
    icon: 'chart',
    link: '/query#aggregate',
    groups: [
      {
        text: 'Aggregate',
        items: [
          { text: 'Pipeline', link: '/query#aggregate' },
          { text: 'Cursor pages', link: '/query#pagination' },
        ],
      },
    ],
  },
  {
    id: 'watch',
    text: 'Watch',
    icon: 'bell',
    link: '/http-api#watch-and-ack',
    groups: [
      {
        text: 'Outbox consumers',
        items: [
          { text: 'watch and ack', link: '/http-api#watch-and-ack' },
          { text: 'Clients', link: '/clients' },
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
          { text: 'Rails', link: '/clients#rails-gem-ndb-record' },
          { text: 'Go', link: '/clients#go-module-github-com-thadeu-ndb-sdk-go' },
          { text: 'TypeScript', link: '/clients#typescript-package-ndb' },
          { text: 'Direct mode', link: '/clients#direct-mode-from-an-app' },
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
          { text: 'Examples', link: '/deployment' },
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
  '/configuration': 'daemon',
  '/http-api': 'daemon',
  '/query': 'query',
  '/clients': 'clients',
  '/operations': 'operations',
  '/docker': 'operations',
  '/deployment': 'deploy',
  '/development': 'deploy',
}
