import type { FC } from 'hono/jsx';

export interface InputProps {
  class?: string;
  mono?: boolean;
  [attribute: string]: unknown;
}

// No field of the console is a login, so password managers must not offer to
// fill or save them: 1Password, LastPass, Bitwarden, and Dashlane.
export const NO_PASSWORD_MANAGER = {
  'data-1p-ignore': true,
  'data-lpignore': 'true',
  'data-bwignore': true,
  'data-form-type': 'other',
};

// Fields hold URLs, JSON, and names, not prose: no red spelling marks. The
// string "false" is on purpose: a boolean false drops the attribute, and a
// textarea then checks spelling by default.
export const noSpelling = { spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off' } as Record<string, string>;

// Input is a text field for forms. It fills the width of its parent.
export const Input: FC<InputProps> = ({ class: extra, mono = false, ...rest }) => {
  const className = ['input', mono ? 'mono' : '', extra ?? ''].filter(Boolean).join(' ');

  return <input class={className} {...NO_PASSWORD_MANAGER} {...noSpelling} {...rest} />;
};
