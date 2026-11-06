import type { FC } from 'hono/jsx';

export interface InputProps {
  class?: string;
  mono?: boolean;
  [attribute: string]: unknown;
}

// Input is a text field for forms. It fills the width of its parent.
export const Input: FC<InputProps> = ({ class: extra, mono = false, ...rest }) => {
  const className = ['input', mono ? 'mono' : '', extra ?? ''].filter(Boolean).join(' ');

  return <input class={className} {...rest} />;
};
