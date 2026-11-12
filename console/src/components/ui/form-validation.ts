// Inline validation for every form of the console. The browser still does
// the checks (required, pattern, type, length), but its popup bubble does not
// show: the message goes under the field, in the style of the console, and
// goes away when the value is right.

type Control = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;

function isControl(target: EventTarget | null): target is Control {
  return target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement;
}

// messageOf gives a short text for the first rule that the value breaks. A
// field can give its own text for a pattern in data-error or title.
export function messageOf(validity: ValidityState, hints: { custom?: string; min?: string; max?: string; minLength?: number; maxLength?: number; fallback?: string }): string {
  if (validity.valueMissing) return 'Fill in this field.';

  if (validity.patternMismatch) return hints.custom || 'This value does not have the right form.';

  if (validity.typeMismatch) return hints.custom || 'This value is not valid.';

  if (validity.tooShort) return `Use ${hints.minLength} characters or more.`;

  if (validity.tooLong) return `Use ${hints.maxLength} characters or less.`;

  if (validity.rangeUnderflow) return `The smallest value is ${hints.min}.`;

  if (validity.rangeOverflow) return `The largest value is ${hints.max}.`;

  if (validity.badInput || validity.stepMismatch) return 'This value is not valid.';

  return hints.fallback || 'This value is not valid.';
}

function textFor(control: Control): string {
  const input = control as HTMLInputElement;

  return messageOf(control.validity, {
    custom: control.dataset.error || control.title,
    min: input.min,
    max: input.max,
    minLength: input.minLength,
    maxLength: input.maxLength,
    fallback: control.validationMessage,
  });
}

function slotOf(control: Control): HTMLElement {
  const anchor = control.closest<HTMLElement>('.dd, .combo, .wherebox') ?? control;
  const next = anchor.nextElementSibling;

  if (next instanceof HTMLElement && next.classList.contains('field-error')) return next;

  const slot = document.createElement('p');

  slot.className = 'field-error';
  slot.id = `error-${Math.random().toString(36).slice(2, 9)}`;
  slot.setAttribute('role', 'alert');
  anchor.after(slot);

  return slot;
}

function show(control: Control): void {
  const slot = slotOf(control);

  slot.textContent = textFor(control);
  slot.hidden = false;

  control.setAttribute('aria-invalid', 'true');
  control.setAttribute('aria-describedby', slot.id);
}

function clear(control: Control): void {
  if (!control.hasAttribute('aria-invalid')) return;

  const slot = slotOf(control);

  slot.hidden = true;
  control.removeAttribute('aria-invalid');
  control.removeAttribute('aria-describedby');
}

export function mount(): void {
  let first = true;

  // invalid fires on each bad field when a form is sent. preventDefault
  // stops the bubble of the browser; the first bad field gets the focus.
  document.addEventListener(
    'invalid',
    event => {
      if (!isControl(event.target)) return;

      event.preventDefault();
      show(event.target);

      if (!first) return;

      first = false;
      event.target.focus();
      event.target.scrollIntoView({ block: 'center', behavior: 'smooth' });

      setTimeout(() => {
        first = true;
      }, 0);
    },
    true,
  );

  const recheck = (event: Event) => {
    const control = event.target;

    if (!isControl(control) || !control.hasAttribute('aria-invalid')) return;

    if (control.validity.valid) clear(control);
    else show(control);
  };

  document.addEventListener('input', recheck, true);
  document.addEventListener('change', recheck, true);
}
