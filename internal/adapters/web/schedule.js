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

// Keep exported skill metadata aligned with the address in the user's browser.
for (const link of document.querySelectorAll('a[data-agent-skill]')) {
  const download = new URL(link.getAttribute('href'), window.location.href);
  download.searchParams.set('origin', window.location.origin);
  link.href = download.href;
}
