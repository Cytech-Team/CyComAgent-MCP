const INSPECTOR_URL = 'http://127.0.0.1:7341/inspector';

const statusDot = document.getElementById('statusDot');
const statusText = document.getElementById('statusText');
const version = document.getElementById('version');
const inspectorBtn = document.getElementById('inspectorBtn');
const reconnectBtn = document.getElementById('reconnectBtn');

function renderStatus(info) {
  const connected = !!info?.connected;
  statusDot.className = `status-dot ${connected ? 'online' : 'offline'}`;
  statusText.textContent = connected ? 'Connected' : (info?.state || 'Disconnected');
  version.textContent = info?.version ? `Bridge v${info.version}` : 'Browser bridge';
}

async function refreshStatus() {
  try {
    const info = await chrome.runtime.sendMessage({type: 'bridge-status'});
    renderStatus(info);
  } catch {
    renderStatus({connected: false, state: 'Unavailable'});
  }
}

inspectorBtn.addEventListener('click', async () => {
  try {
    // Explicit user action: reuse the Inspector tab instead of making duplicates.
    const matches = await chrome.tabs.query({url: INSPECTOR_URL});
    if (matches.length) {
      const existing = matches[0];
      await chrome.tabs.update(existing.id, {active: true});
      if (existing.windowId) await chrome.windows.update(existing.windowId, {focused: true});
    } else {
      await chrome.tabs.create({url: INSPECTOR_URL, active: true});
    }
  } finally {
    window.close();
  }
});

reconnectBtn.addEventListener('click', async () => {
  reconnectBtn.disabled = true;
  try {
    await chrome.runtime.sendMessage({type: 'bridge-reconnect'});
    await new Promise(resolve => setTimeout(resolve, 350));
    await refreshStatus();
  } finally {
    reconnectBtn.disabled = false;
  }
});

refreshStatus();
