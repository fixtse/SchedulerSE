'use strict';

function initializePage(root = document) {
  const frequency = root.querySelector('[name="schedule_mode"]');
  if (frequency && !frequency.dataset.scheduleInitialized) {
    frequency.dataset.scheduleInitialized = 'true';
    const update = () => {
      for (const field of root.querySelectorAll('[data-schedule-modes]')) {
        field.hidden = !field.dataset.scheduleModes.split(' ').includes(frequency.value);
        for (const input of field.querySelectorAll('input, select')) input.disabled = field.hidden;
      }
    };
    frequency.addEventListener('change', update);
    update();
  }

  const kind = root.querySelector('[name="kind"]');
  if (kind && !kind.dataset.initialized) {
    kind.dataset.initialized = 'true';
    const update = () => {
      for (const field of root.querySelectorAll('[data-task-kind]')) {
        field.hidden = field.dataset.taskKind !== kind.value;
        for (const input of field.querySelectorAll('input, textarea')) input.disabled = field.hidden;
      }
    };
    kind.addEventListener('change', update);
    update();
  }

  for (const editor of root.querySelectorAll('.bash-editor')) {
    if (editor.dataset.initialized) continue;
    editor.dataset.initialized = 'true';
    const input = editor.querySelector('textarea');
    const preview = editor.querySelector('pre');
    const highlight = () => {
      const tokens = /#[^\n]*|"(?:\\.|[^"\\])*"|'[^']*'|\$\{[^}]*\}|\$[\w?@#]+|\b(?:if|then|else|elif|fi|for|while|do|done|case|esac|in|function|echo|printf|export|local|return|exit|set|sudo|cd)\b|[|&;<>]+/g;
      const fragment = document.createDocumentFragment();
      let end = 0;
      for (const match of input.value.matchAll(tokens)) {
        fragment.append(document.createTextNode(input.value.slice(end, match.index)));
        const token = document.createElement('span');
        token.className = match[0][0] === '#' ? 'bash-comment' : /^['"]/.test(match[0]) ? 'bash-string' : match[0][0] === '$' ? 'bash-variable' : 'bash-keyword';
        token.textContent = match[0];
        fragment.append(token);
        end = match.index + match[0].length;
      }
      fragment.append(document.createTextNode(input.value.slice(end) + '\n'));
      preview.replaceChildren(fragment);
    };
    const example = editor.parentElement.querySelector('[data-bash-example]');
    if (example) {
      example.hidden = false;
      example.addEventListener('click', () => {
        input.value = [
          '#!/usr/bin/env bash',
          'set -euo pipefail',
          '',
          '# Temporary files are removed after each isolated run.',
          "printf '%s\\n' '{\"message\":\"Hello from SchedulerSE\"}' > /tmp/message.json",
          "jq -e '.message | length > 0' /tmp/message.json > /dev/null",
          ''
        ].join('\n');
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.scrollTop = input.scrollLeft = preview.scrollTop = preview.scrollLeft = 0;
        input.focus();
      });
    }
    input.addEventListener('input', highlight);
    input.addEventListener('scroll', () => {
      preview.scrollTop = input.scrollTop;
      preview.scrollLeft = input.scrollLeft;
    });
    editor.classList.add('highlighted');
    highlight();
  }

  const dialog = document.querySelector('.confirm-dialog');
  for (const form of root.querySelectorAll('[data-confirm-disable]')) {
    if (form.dataset.initialized) continue;
    form.dataset.initialized = 'true';
    form.addEventListener('submit', event => {
      if (form.dataset.confirmed) return;
      event.preventDefault();
      dialog.querySelector('[data-dialog-confirm]').onclick = () => {
        form.dataset.confirmed = 'true';
        dialog.close();
        form.requestSubmit();
      };
      dialog.querySelector('[data-dialog-cancel]').onclick = () => dialog.close();
      dialog.showModal();
    });
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
