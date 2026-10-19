'use strict';
const frequency = document.querySelector('[name="schedule_mode"]');
if (frequency) {
  const update = () => {
    for (const field of document.querySelectorAll('[data-schedule-modes]')) {
      field.hidden = !field.dataset.scheduleModes.split(' ').includes(frequency.value);
    }
  };
  frequency.addEventListener('change', update);
  update();
}
