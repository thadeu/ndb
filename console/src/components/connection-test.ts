import { useConnectionTest } from '@/hooks/useConnectionTest';

// mount upgrades the Test button of each connection form (New and Edit).
// Without the script, the button posts the form and the page shows the
// result after a redirect.
export function mount(): void {
  for (const button of document.querySelectorAll<HTMLButtonElement>('[data-test]')) {
    const form = button.form;
    const result = form?.querySelector<HTMLElement>('[data-test-result]');

    if (!form || !result) continue;

    const test = useConnectionTest(button.getAttribute('formaction') ?? 'connections/test');

    button.addEventListener('click', async event => {
      event.preventDefault();
      result.textContent = 'Testing…';
      result.className = 'test-result';

      const answer = await test(form);

      result.textContent = answer.message;
      result.className = answer.ok ? 'test-result ok' : 'test-result bad';
    });
  }
}
