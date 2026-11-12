import { raw } from 'hono/html';
import type { FC } from 'hono/jsx';

// The paths of each icon, drawn on a 24 by 24 grid with a round stroke.
const PATHS = {
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 6.3"/><path d="M20 4v7h-7"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  'chevron-down': '<path d="M6 9l6 6 6-6"/>',
  'chevron-right': '<path d="M9 6l6 6-6 6"/>',
  'chevron-left': '<path d="M15 6l-6 6 6 6"/>',
  'chevrons-left': '<path d="M12 6l-6 6 6 6"/><path d="M19 6l-6 6 6 6"/>',
  'arrow-left': '<path d="M19 12H5M11 6l-6 6 6 6"/>',
  'arrow-up': '<path d="M12 19V5M6 11l6-6 6 6"/>',
  'arrow-down': '<path d="M12 5v14M6 13l6 6 6-6"/>',
  columns: '<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M10 4v16M15 4v16"/>',
  documents: '<path d="M7 3h7l5 5v13H7z"/><path d="M14 3v5h5"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  sidebar: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>',
  alert: '<circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16v.5"/>',
  search: '<circle cx="11" cy="11" r="6"/><path d="M20 20l-4-4"/>',
  star: '<path d="M12 3l2.7 5.6 6.1.8-4.5 4.3 1.1 6.1L12 16.9 6.6 19.8l1.1-6.1L3.2 9.4l6.1-.8z"/>',
  'chevrons-up-down': '<path d="M8 10l4-4 4 4M8 14l4 4 4-4"/>',
  dashboard: '<path d="M4 4h7v9H4zM13 4h7v5h-7zM13 11h7v9h-7zM4 15h7v5H4z"/>',
  database: '<path d="M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  streams: '<path d="M3 7h14M14 4l3 3-3 3M21 17H7M10 14l-3 3 3 3"/>',
  chart: '<path d="M5 20V10M12 20V4M19 20v-7"/>',
  eye: '<path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  code: '<path d="M8 8l-4 4 4 4M16 8l4 4-4 4M13 5l-2 14"/>',
  history: '<path d="M12 8v4l3 2M4 12a8 8 0 108-8 8 8 0 00-6 2.7M4 4v4h4"/>',
  sliders: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M14 4v4M8 10v4M16 16v4"/>',
} as const;

export type IconName = keyof typeof PATHS;

// iconSvg is the markup of one icon. A browser script uses it when it
// builds an element, such as the button of Select.
export function iconSvg(name: IconName, size = 18): string {
  return `<svg class="icon" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${PATHS[name]}</svg>`;
}

// Icon is a line icon that takes the color of its text. The default size
// reads well next to 14 px text; give size for a bigger or smaller one.
export const Icon: FC<{ name: IconName; size?: number }> = ({ name, size = 18 }) => <>{raw(iconSvg(name, size))}</>;
