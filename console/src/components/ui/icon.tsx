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
