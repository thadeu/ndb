import type { Child, FC } from 'hono/jsx';

// Table scrolls sideways when the screen is narrower than minWidth.
export const Table: FC<{ minWidth?: number; label?: string; children?: Child }> = ({ minWidth = 0, label, children }) => {
  const style = minWidth > 0 ? `min-width:${minWidth}px` : undefined;

  return (
    <div class="scroll">
      <table style={style} aria-label={label}>
        {children}
      </table>
    </div>
  );
};
