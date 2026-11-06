import type { FC } from 'hono/jsx';

export interface TextareaProps {
  class?: string;
  mono?: boolean;
  [attribute: string]: unknown;
}

// Textarea is a multi-line field for forms. It fills the width of its parent.
export const Textarea: FC<TextareaProps> = ({ class: extra, mono = false, children, ...rest }) => {
  const className = ['textarea', mono ? 'mono' : '', extra ?? ''].filter(Boolean).join(' ');

  return (
    <textarea class={className} {...rest}>
      {children}
    </textarea>
  );
};
