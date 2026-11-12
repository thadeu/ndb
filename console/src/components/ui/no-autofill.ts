// The console has no login, so password managers (1Password, LastPass,
// Bitwarden, Dashlane) must not offer to fill or save any field, and the
// browser must not suggest past values or fix the spelling of JSON. The
// server marks the fields of the design system (Input); this script marks
// every other field, also the ones that a script adds later, such as the
// editors of Dev Tools and of the document drawer.
const MARKS: Record<string, string> = {
  'data-1p-ignore': 'true',
  'data-lpignore': 'true',
  'data-bwignore': 'true',
  'data-form-type': 'other',
  autocomplete: 'off',
  autocorrect: 'off',
  autocapitalize: 'off',
  spellcheck: 'false',
};

const FIELDS = 'input:not([type="checkbox"]):not([type="radio"]):not([type="hidden"]):not([type="range"]), textarea';

function mark(field: Element): void {
  for (const [name, value] of Object.entries(MARKS)) {
    if (!field.hasAttribute(name)) field.setAttribute(name, value);
  }
}

function markAll(root: ParentNode): void {
  if (root instanceof Element && root.matches(FIELDS)) mark(root);

  for (const field of root.querySelectorAll(FIELDS)) mark(field);
}

export function mount(): void {
  markAll(document);

  new MutationObserver(records => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (node instanceof Element) markAll(node);
      }
    }
  }).observe(document.body, { subtree: true, childList: true });
}
