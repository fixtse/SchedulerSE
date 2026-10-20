'use strict';

function initializePage(root = document) {
  const frequency = root.querySelector('[name="schedule_mode"]');
  if (frequency && !frequency.dataset.scheduleInitialized) {
    frequency.dataset.scheduleInitialized = 'true';
    const update = () => {
      for (const field of root.querySelectorAll('[data-schedule-modes]')) {
        field.hidden = !field.dataset.scheduleModes.split(' ').includes(frequency.value);
      }
    };
    frequency.addEventListener('change', update);
    update();
  }

  // Keep exported skill metadata aligned with the address in the user's browser.
  for (const link of root.querySelectorAll('a[data-agent-skill]')) {
    const download = new URL(link.getAttribute('href'), window.location.href);
    download.searchParams.set('origin', window.location.origin);
    link.href = download.href;
  }
}

initializePage();
document.addEventListener('htmx:load', event => initializePage(event.detail.elt));

// Failed enhanced navigation retains ordinary browser navigation/error behavior.
// Forms and downloads are not boosted, so this cannot replay a submission.
function fallbackNavigation(event) {
  const request = event.detail.requestConfig;
  if (!request || String(request.verb || '').toLowerCase() !== 'get') return;
  const url = new URL(request.path, window.location.href);
  if (url.origin === window.location.origin) window.location.assign(url.href);
}
for (const type of ['htmx:sendError', 'htmx:timeout', 'htmx:responseError']) {
  document.addEventListener(type, fallbackNavigation);
}
