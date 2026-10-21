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

  const delivery = root.querySelector('[name="delivery_mode"]');
  if (delivery && !delivery.dataset.deliveryInitialized) {
    delivery.dataset.deliveryInitialized = 'true';
    const update = () => {
      for (const fields of root.querySelectorAll('[data-webhook-fields]')) {
        fields.hidden = delivery.value !== 'webhook';
      }
    };
    delivery.addEventListener('change', update);
    update();
  }

  const mcpURL = new URL('/api/agent/v1/mcp', window.location.origin).href;
  for (const input of root.querySelectorAll('[data-mcp-url]')) input.value = mcpURL;
  for (const config of root.querySelectorAll('[data-mcp-config]')) {
    config.textContent = JSON.stringify({mcpServers: {schedulerse: {
      url: mcpURL, headers: {Authorization: 'Bearer YOUR_API_TOKEN'}
    }}}, null, 2);
  }

  // Keep exported delivery kit metadata aligned with the address in the user's browser.
  for (const link of root.querySelectorAll('a[data-agent-delivery]')) {
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
