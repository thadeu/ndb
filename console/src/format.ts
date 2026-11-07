// Number formats for the pages. Sizes use powers of 1024, counts use k and M.

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

export function formatBytes(value: number): string {
  let size = value;
  let unit = 0;

  while (size >= 1024 && unit < UNITS.length - 1) {
    size /= 1024;
    unit++;
  }

  const digits = unit === 0 || size >= 100 ? 0 : 1;

  return `${size.toFixed(digits)} ${UNITS[unit]}`;
}

export function formatCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;

  if (value >= 10_000) return `${(value / 1000).toFixed(1)}k`;

  return value.toLocaleString('en-US');
}

export function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);

  if (days > 0) return `${days}d ${hours}h`;

  if (hours > 0) return `${hours}h ${minutes}m`;

  return `${minutes}m`;
}

// formatVersion adds "v" to a release number (0.3.1 → v0.3.1) and keeps a
// build name such as dev as it is.
export function formatVersion(version: string): string {
  return /^\d/.test(version) ? `v${version}` : version;
}
