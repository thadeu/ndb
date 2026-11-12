import { atValue, seconds } from '@/pitr';

// mount ties the range slider of the restore card to the date picker: a move
// of the slider writes the time in the field, and a new time in the field
// moves the slider. Without the script the field alone sets the time.
export function mount(): void {
  const slider = document.querySelector<HTMLInputElement>('[data-pitr-slider]');
  const input = slider ? document.getElementById(slider.dataset.pitrSlider ?? '') : null;

  if (!slider || !(input instanceof HTMLInputElement)) return;

  slider.hidden = false;

  slider.addEventListener('input', () => {
    input.value = atValue(new Date(Number(slider.value) * 1000));
  });

  const follow = () => {
    const at = seconds(input.value);

    if (at !== undefined) slider.value = String(at);
  };

  input.addEventListener('input', follow);
  input.addEventListener('change', follow);
}
