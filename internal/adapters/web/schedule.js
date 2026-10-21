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
    // Use the public address seen by this browser, including HTTPS behind a proxy.
    config.textContent = config.textContent.replace(
      JSON.stringify(config.dataset.mcpEndpoint), JSON.stringify(mcpURL));
    config.dataset.mcpEndpoint = mcpURL;
  }

  for (const picker of root.querySelectorAll('[data-mcp-client]')) {
    if (picker.dataset.initialized) continue;
    picker.dataset.initialized = 'true';
    const container = picker.closest('.mcp-configuration-body');
    const update = () => {
      for (const panel of container.querySelectorAll('[data-mcp-panel]')) {
        panel.hidden = panel.dataset.mcpPanel !== picker.value;
      }
    };
    container.querySelector('[data-mcp-picker]').hidden = false;
    picker.addEventListener('change', update);
    update();
  }

  for (const button of root.querySelectorAll('[data-copy-config]')) {
    if (button.dataset.initialized) continue;
    button.dataset.initialized = 'true';
    button.hidden = false;
    button.addEventListener('click', async () => {
      const panel = button.closest('[data-mcp-panel]');
      const config = panel.querySelector('[data-mcp-config]');
      const status = panel.querySelector('.mcp-copy-status');
      try {
        await navigator.clipboard.writeText(config.textContent);
        status.textContent = 'Configuration copied.';
      } catch {
        // Clipboard API may be unavailable on self-hosted HTTP installations.
        const range = document.createRange();
        range.selectNodeContents(config);
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        let copied = false;
        try { copied = document.execCommand('copy'); } catch { /* Keep selection for manual copy. */ }
        status.textContent = copied ? 'Configuration copied.' : 'Configuration selected. Press Ctrl+C or ⌘C to copy.';
      }
    });
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
