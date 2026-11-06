import { useConnectionTest } from '@/hooks/useConnectionTest';

// mount upgrades the Test button of the connection form. Without the script,
// the button posts the form and the page shows the result after a redirect.
export function mount(): void {
  const button = document.querySelector<HTMLButtonElement>('[data-test]');
  const result = document.querySelector<HTMLElement>('[data-test-result]');
  const form = button?.form;

  if (!button || !result || !form) return;

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
