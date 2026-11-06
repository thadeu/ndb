import type { Child, FC } from 'hono/jsx';

export type ButtonVariant = 'primary' | 'secondary' | 'dark' | 'ghost' | 'destructive';
export type ButtonSize = 'sm' | 'md';

export interface ButtonProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  href?: string;
  class?: string;
  children?: Child;
  [attribute: string]: unknown;
}

export function buttonClass(variant: ButtonVariant, size: ButtonSize, extra?: string): string {
  return ['btn', variant, size === 'sm' ? 'small' : '', extra ?? ''].filter(Boolean).join(' ');
}

// Button renders a link when it has href, and a button in other cases. A
// button in a form submits it, unless type says otherwise.
export const Button: FC<ButtonProps> = ({ variant = 'secondary', size = 'md', href, class: extra, children, ...rest }) => {
  const className = buttonClass(variant, size, extra);

  if (href) {
    return (
      <a class={className} href={href} {...rest}>
        {children}
      </a>
    );
  }

  return (
    <button class={className} {...rest}>
      {children}
    </button>
  );
};
