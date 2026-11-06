import type { Child, FC } from 'hono/jsx';

// Field puts a label and a control together. The control must use id as its id.
export const Field: FC<{ id: string; label: string; children?: Child }> = ({ id, label, children }) => (
  <div class="form-field">
    <label class="label" for={id}>
      {label}
    </label>
    {children}
  </div>
);
