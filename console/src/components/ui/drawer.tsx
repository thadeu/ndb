import type { FC } from 'hono/jsx';

import { Dialog, type DialogProps } from './dialog';

// Drawer is a dialog that slides in from the right edge, or up from the
// bottom. It uses the Dialog script: openDialog(id) opens it, and Esc or the
// backdrop closes it.
export const Drawer: FC<DialogProps & { side?: 'right' | 'bottom' }> = ({ side = 'right', ...props }) => {
  const kind = `drawer drawer-${side}`;

  return <Dialog {...props} class={props.class ? `${kind} ${props.class}` : kind} />;
};
