// Where a popover goes next to its trigger: below it when it fits or when
// there is more room below than above, else above; its start edge on the
// trigger's start edge (or end on end), moved back inside the viewport. On a
// narrow screen (sheetBelow) it is a sheet on the bottom edge, full width.
// The numbers come from getBoundingClientRect, so the result is for
// position: fixed.

export interface Rect {
  top: number;
  left: number;
  right: number;
  bottom: number;
}

export interface Size {
  width: number;
  height: number;
}

export type Placement = 'bottom-start' | 'bottom-end' | 'top-start' | 'top-end';


export interface Placed {
  top: number;
  left: number;
  maxHeight: number;
  placement: Placement | 'sheet';
}

export interface PlaceOptions {
  prefer?: Placement;
  gap?: number;
  margin?: number;
  sheetBelow?: number;
}

export function place(trigger: Rect, size: Size, viewport: Size, options: PlaceOptions = {}): Placed {
  const { prefer = 'bottom-start', gap = 6, margin = 8, sheetBelow = 520 } = options;

  if (viewport.width < sheetBelow) {
    const room = viewport.height - margin;

    return { top: viewport.height - Math.min(size.height, room), left: 0, maxHeight: room, placement: 'sheet' };
  }

  const below = viewport.height - trigger.bottom - gap - margin;
  const above = trigger.top - gap - margin;
  const wantsTop = prefer.startsWith('top');
  const fitsPreferred = wantsTop ? size.height <= above : size.height <= below;
  const top = fitsPreferred ? wantsTop : above > below && size.height > below;

  const room = Math.max(top ? above : below, 0);
  const height = Math.min(size.height, room);
  const y = top ? trigger.top - gap - height : trigger.bottom + gap;

  const end = prefer.endsWith('end');
  const width = Math.min(size.width, viewport.width - 2 * margin);
  const start = end ? trigger.right - width : trigger.left;
  const x = Math.min(Math.max(start, margin), viewport.width - margin - width);

  return { top: y, left: x, maxHeight: room, placement: `${top ? 'top' : 'bottom'}-${end ? 'end' : 'start'}` };
}
