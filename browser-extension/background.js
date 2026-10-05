const PORT = 17373;
const BRIDGE_VERSION = '0.3.1';
const HEARTBEAT_MS = 20000;
const REQUEST_TIMEOUT_MS = 15000;
const MAX_REQUEST_BYTES = 1 << 20;
const MAX_RESPONSE_BYTES = 16 << 20;
const byteLength = (text) => new TextEncoder().encode(text).byteLength;

function sendResponse(socket, response) {
  let payload = JSON.stringify(response);
  if (byteLength(payload) > MAX_RESPONSE_BYTES) {
    payload = JSON.stringify({id:response.id, ok:false, error:'Response exceeds 16 MiB; action outcome may be unknown'});
  }
  if (socket.readyState === WebSocket.OPEN) socket.send(payload);
}
const RECONNECT_ALARM = 'cycom-browser-bridge-reconnect';
const DEBUGGER_PROTOCOL = '1.3';
let ws;
let retry;
let heartbeat;

function stopHeartbeat() {
  if (heartbeat !== undefined) {
    clearInterval(heartbeat);
    heartbeat = undefined;
  }
}

function startHeartbeat(socket) {
  stopHeartbeat();
  heartbeat = setInterval(() => {
    if (ws !== socket || socket.readyState !== WebSocket.OPEN) return;
    try { socket.send(JSON.stringify({type:'ping'})); }
    catch { socket.close(); }
  }, HEARTBEAT_MS);
}

function withTimeout(promise, ms, label) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms; action outcome may be unknown`)), ms);
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function connect() {
  clearTimeout(retry);
  retry = undefined;
  if (ws && ws.readyState !== WebSocket.CLOSED) return;
  const socket = new WebSocket(`ws://127.0.0.1:${PORT}/extension`);
  ws = socket;
  socket.onopen = () => {
    if (ws !== socket) return socket.close();
    socket.send(JSON.stringify({type:'hello', version:BRIDGE_VERSION}));
    startHeartbeat(socket);
  };
  socket.onclose = () => {
    if (ws !== socket) return;
    ws = undefined;
    stopHeartbeat();
    retry = setTimeout(connect, 1500);
  };
  socket.onerror = () => socket.close();
  socket.onmessage = async (ev) => {
    if (typeof ev.data !== 'string' || byteLength(ev.data) > MAX_REQUEST_BYTES) {
      socket.close(1009, 'Request exceeds 1 MiB');
      return;
    }
    let req;
    try { req = JSON.parse(ev.data); } catch { return; }
    if (!req.id) return;
    try {
      const result = await withTimeout(dispatch(req), REQUEST_TIMEOUT_MS, req.action || 'request');
      sendResponse(socket, {id:req.id, ok:true, result});
    } catch (e) {
      sendResponse(socket, {id:req.id, ok:false, error:String(e?.message || e)});
    }
  };
}

function ensureConnection() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  if (ws) {
    try { ws.close(); } catch {}
    ws = undefined;
  }
  connect();
}

function armReconnectAlarm() {
  chrome.alarms.create(RECONNECT_ALARM, { periodInMinutes: 0.5 });
}

async function tab(tabId) {
  if (tabId) return chrome.tabs.get(tabId);
  const [t] = await chrome.tabs.query({active:true, lastFocusedWindow:true});
  if (!t) throw new Error('No active tab');
  return t;
}

async function focusTab(t) {
  await chrome.tabs.update(t.id, {active:true});
  if (t.windowId) {
    await chrome.windows.update(t.windowId, {focused:true}).catch(() => {});
  }
}

async function waitForTabComplete(tabId, timeoutMs=10000) {
  const current = await chrome.tabs.get(tabId).catch(() => null);
  if (!current || current.status === 'complete') return current;
  return new Promise((resolve) => {
    let done = false;
    const finish = async () => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      chrome.tabs.onUpdated.removeListener(listener);
      resolve(await chrome.tabs.get(tabId).catch(() => null));
    };
    const listener = (id, info) => {
      if (id === tabId && info.status === 'complete') finish();
    };
    const timer = setTimeout(finish, timeoutMs);
    chrome.tabs.onUpdated.addListener(listener);
  });
}

async function exec(tabId, func, args=[], allFrames=false) {
  const result = await chrome.scripting.executeScript({
    target: {tabId, allFrames},
    func,
    args
  });
  return allFrames ? result : result[0]?.result;
}

const debuggerTabs = new Set();
const debuggerLocks = new Map();

function serializeDebugger(tabId, fn) {
  const previous = debuggerLocks.get(tabId) || Promise.resolve();
  const next = previous.catch(() => {}).then(fn);
  debuggerLocks.set(tabId, next);
  return next.finally(() => {
    if (debuggerLocks.get(tabId) === next) debuggerLocks.delete(tabId);
  });
}

async function ensureDebugger(tabId) {
  const target = {tabId};
  if (debuggerTabs.has(tabId)) return target;

  // A MV3 service worker can be suspended while Chrome keeps its debugger
  // attachment alive. Probe first so a resumed worker reuses that session
  // instead of failing with "Another debugger is already attached".
  try {
    await chrome.debugger.sendCommand(target, 'Runtime.enable');
    debuggerTabs.add(tabId);
    return target;
  } catch {}

  try {
    await chrome.debugger.attach(target, DEBUGGER_PROTOCOL);
    debuggerTabs.add(tabId);
    return target;
  } catch (e) {
    const message = String(e?.message || e);
    if (!message.includes('Another debugger is already attached')) throw e;

    // The attachment may belong to this same extension from before service
    // worker suspension. If so, sendCommand succeeds and ownership is safe to
    // reuse. If another debugger (for example DevTools) owns the target, keep
    // that ownership intact and return a clear error.
    try {
      await chrome.debugger.sendCommand(target, 'Runtime.enable');
      debuggerTabs.add(tabId);
      return target;
    } catch {
      throw new Error(`tab ${tabId} is controlled by another debugger; close DevTools or the competing debugger and retry`);
    }
  }
}

async function withDebugger(tabId, fn) {
  return serializeDebugger(tabId, async () => fn(await ensureDebugger(tabId)));
}

chrome.debugger.onDetach.addListener((source) => {
  if (source?.tabId) debuggerTabs.delete(source.tabId);
});
chrome.tabs.onRemoved.addListener((tabId) => {
  debuggerTabs.delete(tabId);
  debuggerLocks.delete(tabId);
});

function mouseButton(button) {
  if (button === 2 || button === 'middle') return 'middle';
  if (button === 3 || button === 'right') return 'right';
  return 'left';
}

async function viewportPoint(tabId) {
  return exec(tabId, () => ({x: Math.round(innerWidth / 2), y: Math.round(innerHeight / 2)}));
}

async function resolvePoint(t, r) {
  if (Number.isFinite(r.x) && Number.isFinite(r.y)) return {x:r.x, y:r.y};
  if (r.frameId !== undefined && r.frameId !== 0) {
    throw new Error('Semantic pointer targets are supported only in the top frame; use screenshot viewport coordinates for child frames');
  }
  return exec(t.id, browserPage, ['resolve', {
    selector:r.selector, text:r.action === 'type' ? undefined : r.text,
    index:r.index, snapshotId:r.snapshotId
  }]);
}

function browserPage(mode='state', target={}) {
  const LIMITS = {
    text: 120000,
    nodes: 2500,
    interactive: 600,
    links: 500,
    headings: 300,
    images: 300,
    tables: 80,
    forms: 120
  };
  const clean = (value, max=1200) => String(value ?? '').replace(/\s+/g, ' ').trim().slice(0, max);
  const visible = (e) => {
    if (!e.isConnected) return false;
    const rect = e.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return false;
    for (let node=e; node; node=node.parentElement || node.getRootNode()?.host) {
      const style = getComputedStyle(node);
      if (style.visibility === 'hidden' || style.visibility === 'collapse' || style.display === 'none' ||
          Number(style.opacity) === 0 || node.hidden || node.inert || node.getAttribute('aria-hidden') === 'true') return false;
    }
    return true;
  };
  const interactiveSelector = 'a[href],button,input,textarea,select,[role="button"],[role="link"],[role="checkbox"],[role="radio"],[role="tab"],[contenteditable="true"],[tabindex]';
  const signature = (e) => JSON.stringify([e.tagName, e.id, e.getAttribute('role'), e.href, e.type,
    clean(e.getAttribute('aria-label') || e.getAttribute('title') || e.getAttribute('alt') || e.innerText || e.value || '', 500)]);
  if (mode === 'resolve' && Number.isInteger(target.index)) {
    const snapshot = globalThis.__cycomTargetSnapshot;
    if (!target.snapshotId || snapshot?.id !== target.snapshotId) throw new Error('Target snapshot is missing or stale; request state again');
    const observed = snapshot.elements[target.index];
    const element = observed?.element;
    if (!element || !visible(element)) throw new Error('Observed target is missing or hidden');
    if (signature(element) !== observed.signature) throw new Error('Observed target changed; request state again');
    if (target.selector !== undefined || target.text !== undefined) throw new Error('Use either an observed index or selector/text targeting');
    return pointFor(element);
  }
  function pointFor(element) {
    if (element.disabled || element.getAttribute('aria-disabled') === 'true' || getComputedStyle(element).pointerEvents === 'none') {
      throw new Error('Target is disabled or does not accept pointer input');
    }
    element.scrollIntoView({block:'center', inline:'center', behavior:'instant'});
    const rect = element.getBoundingClientRect();
    const left = Math.max(0, rect.left), right = Math.min(innerWidth, rect.right);
    const top = Math.max(0, rect.top), bottom = Math.min(innerHeight, rect.bottom);
    if (right <= left || bottom <= top) throw new Error('Target is outside the viewport');
    const x = (left + right) / 2, y = (top + bottom) / 2;
    let hit = document.elementFromPoint(x, y);
    while (hit?.shadowRoot) {
      const deeper = hit.shadowRoot.elementFromPoint(x, y);
      if (!deeper || deeper === hit) break;
      hit = deeper;
    }
    for (let node=hit; node; node=node.parentElement || node.getRootNode()?.host) {
      if (node === element) return {x, y};
    }
    throw new Error('Target is obscured by another element');
  }
  const roots = [document];
  const all = [];
  for (let ri = 0; ri < roots.length && all.length < LIMITS.nodes; ri++) {
    const walker = document.createTreeWalker(roots[ri], NodeFilter.SHOW_ELEMENT);
    let e;
    while ((e = walker.nextNode()) && all.length < LIMITS.nodes) {
      all.push(e);
      if (e.shadowRoot) roots.push(e.shadowRoot);
    }
  }
  const interactiveElements = all.filter((e) => e.matches?.(interactiveSelector) && visible(e)).slice(0, LIMITS.interactive);
  if (mode === 'resolve') {
    if (target.index !== undefined) throw new Error('index must be an integer');
    if (target.snapshotId !== undefined) throw new Error('snapshotId requires index');
    if (target.selector === undefined && target.text === undefined) throw new Error('Explicit coordinates, selector, text, or observed index are required');
    let candidates = target.selector !== undefined
      ? all.filter((e) => e.matches(target.selector) && visible(e))
      : interactiveElements;
    if (target.text !== undefined) {
      const needle = String(target.text).trim().toLowerCase();
      if (!needle) throw new Error('Target text must not be empty');
      const label = (e) => clean(e.innerText || e.value || e.getAttribute('aria-label') || e.getAttribute('title') || '').toLowerCase();
      const exact = candidates.filter((e) => label(e) === needle);
      candidates = exact.length ? exact : candidates.filter((e) => label(e).includes(needle));
    }
    if (candidates.length !== 1) throw new Error(candidates.length ? 'Target is ambiguous; use an observed index or viewport coordinates' : 'Explicit target not found');
    return pointFor(candidates[0]);
  }
  const snapshotId = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
  globalThis.__cycomTargetSnapshot = {id:snapshotId, elements:interactiveElements.map(element => ({element, signature:signature(element)}))};
  const info = (e, i) => {
    const rect = e.getBoundingClientRect();
    return {
      i,
      tag: e.tagName?.toLowerCase() || '',
      id: e.id || '',
      role: e.getAttribute?.('role') || '',
      name: clean(e.getAttribute?.('aria-label') || e.getAttribute?.('title') || e.getAttribute?.('alt') || e.innerText || e.value || '', 500),
      text: clean(e.innerText || e.textContent || e.value || '', 800),
      type: e.type || '',
      href: e.href || '',
      disabled: !!e.disabled,
      checked: !!e.checked,
      selected: !!e.selected,
      editable: !!e.isContentEditable || ['INPUT','TEXTAREA','SELECT'].includes(e.tagName),
      visible: visible(e),
      rect: {x:rect.x, y:rect.y, w:rect.width, h:rect.height}
    };
  };
  const interactive = interactiveElements.map(info);
  const links = all.filter((e) => e.tagName === 'A' && e.href).slice(0, LIMITS.links).map((e) => ({
    text: clean(e.innerText || e.textContent || e.getAttribute('aria-label') || '', 500),
    href: e.href,
    visible: visible(e)
  }));
  const headings = all.filter((e) => /^H[1-6]$/.test(e.tagName)).slice(0, LIMITS.headings).map((e) => ({
    level: Number(e.tagName.slice(1)),
    text: clean(e.innerText || e.textContent || '', 1000)
  }));
  const images = all.filter((e) => e.tagName === 'IMG').slice(0, LIMITS.images).map((e) => ({
    src: e.currentSrc || e.src || '',
    alt: clean(e.alt || '', 500),
    width: e.naturalWidth || e.width || 0,
    height: e.naturalHeight || e.height || 0,
    visible: visible(e)
  }));
  const tables = all.filter((e) => e.tagName === 'TABLE').slice(0, LIMITS.tables).map((table) => ({
    caption: clean(table.caption?.innerText || '', 500),
    rows: [...table.rows].slice(0, 100).map((row) => [...row.cells].slice(0, 40).map((cell) => clean(cell.innerText || cell.textContent || '', 1000)))
  }));
  const forms = all.filter((e) => e.tagName === 'FORM').slice(0, LIMITS.forms).map((form) => ({
    action: form.action || '',
    method: form.method || '',
    text: clean(form.innerText || '', 3000)
  }));
  const active = document.activeElement;
  return {
    snapshotId,
    url: location.href,
    title: document.title,
    fullText: clean(document.body?.innerText || document.documentElement?.innerText || '', LIMITS.text),
    headings,
    links,
    images,
    tables,
    forms,
    interactive,
    viewport: {
      width: innerWidth,
      height: innerHeight,
      scrollX,
      scrollY,
      documentWidth: document.documentElement?.scrollWidth || 0,
      documentHeight: document.documentElement?.scrollHeight || 0
    },
    selection: clean(getSelection?.()?.toString?.() || '', 5000),
    activeElement: active ? info(active, -1) : null,
    shadowRootCount: all.filter((e) => !!e.shadowRoot).length
  };
}

async function captureScreenshot(t) {
  await focusTab(t);
  const metrics = await exec(t.id, () => ({
    width: innerWidth,
    height: innerHeight,
    devicePixelRatio: devicePixelRatio || 1
  }));
  const dataUrl = await chrome.tabs.captureVisibleTab(t.windowId, {format:'png'});
  const comma = dataUrl.indexOf(',');
  if (comma < 0) throw new Error('captureVisibleTab returned an invalid data URL');
  return {
    tabId: t.id,
    title: t.title,
    url: t.url,
    mimeType: 'image/png',
    data: dataUrl.slice(comma + 1),
    width: Math.round(metrics?.width || 0),
    height: Math.round(metrics?.height || 0),
    pixelWidth: Math.round((metrics?.width || 0) * (metrics?.devicePixelRatio || 1)),
    pixelHeight: Math.round((metrics?.height || 0) * (metrics?.devicePixelRatio || 1)),
    scale: metrics?.devicePixelRatio || 1
  };
}

function keySpec(raw) {
  const aliases = {
    ENTER:{key:'Enter',code:'Enter',vk:13},
    RETURN:{key:'Enter',code:'Enter',vk:13},
    TAB:{key:'Tab',code:'Tab',vk:9},
    ESC:{key:'Escape',code:'Escape',vk:27},
    ESCAPE:{key:'Escape',code:'Escape',vk:27},
    BACKSPACE:{key:'Backspace',code:'Backspace',vk:8},
    DELETE:{key:'Delete',code:'Delete',vk:46},
    SPACE:{key:' ',code:'Space',vk:32},
    ARROWUP:{key:'ArrowUp',code:'ArrowUp',vk:38},
    ARROWDOWN:{key:'ArrowDown',code:'ArrowDown',vk:40},
    ARROWLEFT:{key:'ArrowLeft',code:'ArrowLeft',vk:37},
    ARROWRIGHT:{key:'ArrowRight',code:'ArrowRight',vk:39},
    HOME:{key:'Home',code:'Home',vk:36},
    END:{key:'End',code:'End',vk:35},
    PAGEUP:{key:'PageUp',code:'PageUp',vk:33},
    PAGEDOWN:{key:'PageDown',code:'PageDown',vk:34}
  };
  const text = String(raw || '').trim();
  const upper = text.toUpperCase();
  if (aliases[upper]) return aliases[upper];
  if (text.length === 1) {
    const ch = text;
    const up = ch.toUpperCase();
    return {
      key: ch,
      code: /[A-Z]/.test(up) ? `Key${up}` : /[0-9]/.test(ch) ? `Digit${ch}` : '',
      vk: up.charCodeAt(0)
    };
  }
  return {key:text, code:text, vk:0};
}

function parseKeypress(r) {
  const raw = Array.isArray(r.keys) && r.keys.length ? r.keys : String(r.key || '').split('+').filter(Boolean);
  if (!raw.length) throw new Error('key or keys is required');
  let modifiers = 0;
  let base = null;
  for (const part of raw) {
    const u = String(part).trim().toUpperCase();
    if (u === 'ALT' || u === 'OPTION') modifiers |= 1;
    else if (u === 'CTRL' || u === 'CONTROL') modifiers |= 2;
    else if (u === 'META' || u === 'CMD' || u === 'COMMAND' || u === 'SUPER') modifiers |= 4;
    else if (u === 'SHIFT') modifiers |= 8;
    else base = part;
  }
  if (!base) throw new Error('keypress requires a non-modifier key');
  return {modifiers, ...keySpec(base)};
}

async function dispatch(r) {
  if (r.action === 'tabs') {
    return chrome.tabs.query({}).then(xs => xs.map(t => ({
      id:t.id, title:t.title, url:t.url, active:t.active, windowId:t.windowId, bridgeVersion:BRIDGE_VERSION
    })));
  }
  if (r.action === 'open') {
    const t = await chrome.tabs.create({url:r.url || 'about:blank', active:r.active !== false});
    await waitForTabComplete(t.id);
    return {id:t.id, title:t.title, url:t.url, active:t.active};
  }

  const t = await tab(r.tabId);

  if (r.action === 'switch') {
    await chrome.tabs.update(t.id, {active:true});
    if (t.windowId) await chrome.windows.update(t.windowId, {focused:true}).catch(() => {});
    return {tabId:t.id};
  }
  if (r.action === 'close') {
    await chrome.tabs.remove(t.id);
    return {tabId:t.id, closed:true};
  }
  if (r.action === 'navigate') {
    if (!r.url) throw new Error('url is required');
    await chrome.tabs.update(t.id, {url:r.url});
    const updated = await waitForTabComplete(t.id);
    return {tabId:t.id, url:updated?.url || r.url, title:updated?.title || ''};
  }
  if (r.action === 'back') {
    await chrome.tabs.goBack(t.id);
    const updated = await waitForTabComplete(t.id);
    return {tabId:t.id, url:updated?.url || '', title:updated?.title || ''};
  }
  if (r.action === 'forward') {
    await chrome.tabs.goForward(t.id);
    const updated = await waitForTabComplete(t.id);
    return {tabId:t.id, url:updated?.url || '', title:updated?.title || ''};
  }
  if (r.action === 'reload') {
    await chrome.tabs.reload(t.id);
    const updated = await waitForTabComplete(t.id);
    return {tabId:t.id, url:updated?.url || '', title:updated?.title || ''};
  }
  if (r.action === 'state') {
    const frames = await exec(t.id, browserPage, [], true);
    const renderedFrames = frames.filter(x => x.result).map(x => ({
      frameId:x.frameId,
      documentId:x.documentId,
      ...x.result
    }));
    return {
      tabId:t.id,
      title:t.title,
      url:t.url,
      bridgeVersion:BRIDGE_VERSION,
      frameCount:renderedFrames.length,
      ...renderedFrames.find(frame => frame.frameId === 0),
      frames:renderedFrames
    };
  }
  if (r.action === 'screenshot') return captureScreenshot(t);
  if (r.action === 'wait') {
    await new Promise(resolve => setTimeout(resolve, Math.max(0, Math.min(Number(r.ms) || 1000, 15000))));
    return {tabId:t.id, waitedMs:Math.max(0, Math.min(Number(r.ms) || 1000, 15000))};
  }

  if (['move','click','double_click','drag','scroll','type','keypress'].includes(r.action)) {
    await focusTab(t);
  }

  if (r.action === 'move') {
    return withDebugger(t.id, async (target) => {
      const p = await resolvePoint(t, r);
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y});
      return {tabId:t.id, x:p.x, y:p.y};
    });
  }

  if (r.action === 'click' || r.action === 'double_click') {
    const button = mouseButton(r.button);
    const count = r.action === 'double_click' ? 2 : 1;
    return withDebugger(t.id, async (target) => {
      const p = await resolvePoint(t, r);
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y});
      for (let i = 1; i <= count; i++) {
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:p.x, y:p.y, button, clickCount:i});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:p.x, y:p.y, button, clickCount:i});
      }
      return {tabId:t.id, x:p.x, y:p.y, button, clickCount:count};
    });
  }

  if (r.action === 'drag') {
    let path = Array.isArray(r.path) ? r.path.filter(p => Number.isFinite(p?.x) && Number.isFinite(p?.y)) : [];
    if (path.length < 2 && [r.startX,r.startY,r.endX,r.endY].every(Number.isFinite)) {
      path = [{x:r.startX,y:r.startY},{x:r.endX,y:r.endY}];
    }
    if (path.length < 2) throw new Error('drag requires path with at least 2 points or start/end coordinates');
    const button = mouseButton(r.button);
    return withDebugger(t.id, async (target) => {
      const first = path[0];
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:first.x, y:first.y});
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:first.x, y:first.y, button, clickCount:1});
      for (const p of path.slice(1)) {
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y, button, buttons:1});
      }
      const last = path[path.length - 1];
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:last.x, y:last.y, button, clickCount:1});
      return {tabId:t.id, button, path};
    });
  }

  if (r.action === 'scroll') {
    const explicitDelta = Number.isFinite(r.deltaX) || Number.isFinite(r.deltaY) ||
      Number.isFinite(r.xDelta) || Number.isFinite(r.yDelta);
    const p = explicitDelta && Number.isFinite(r.x) && Number.isFinite(r.y)
      ? {x:r.x, y:r.y}
      : await viewportPoint(t.id);
    const deltaX = Number(r.deltaX ?? r.xDelta ?? (explicitDelta ? 0 : r.x) ?? 0) || 0;
    const deltaY = Number(r.deltaY ?? r.yDelta ?? (explicitDelta ? 600 : r.y) ?? 600) || 0;
    return withDebugger(t.id, async (target) => {
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {
        type:'mouseWheel', x:p.x, y:p.y, deltaX, deltaY
      });
      return {tabId:t.id, x:p.x, y:p.y, deltaX, deltaY};
    });
  }

  if (r.action === 'type') {
    return withDebugger(t.id, async (target) => {
      if (['selector','index','snapshotId','frameId','x','y'].some(key => r[key] !== undefined)) {
        const p = await resolvePoint(t, r);
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:p.x, y:p.y, button:'left', clickCount:1});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:p.x, y:p.y, button:'left', clickCount:1});
      }
      await chrome.debugger.sendCommand(target, 'Input.insertText', {text:String(r.text ?? '')});
      return {tabId:t.id, typed:String(r.text ?? '').length};
    });
  }

  if (r.action === 'keypress') {
    const spec = parseKeypress(r);
    return withDebugger(t.id, async (target) => {
      const params = {
        key:spec.key,
        code:spec.code,
        windowsVirtualKeyCode:spec.vk,
        nativeVirtualKeyCode:spec.vk,
        modifiers:spec.modifiers
      };
      await chrome.debugger.sendCommand(target, 'Input.dispatchKeyEvent', {type:'rawKeyDown', ...params});
      await chrome.debugger.sendCommand(target, 'Input.dispatchKeyEvent', {type:'keyUp', ...params});
      return {tabId:t.id, key:spec.key, modifiers:spec.modifiers};
    });
  }

  throw new Error(`Unknown action: ${r.action}`);
}

chrome.runtime.onMessage.addListener((message) => {
  if (message?.type === 'bridge-status') {
    ensureConnection();
    return Promise.resolve({
      connected: !!ws && ws.readyState === WebSocket.OPEN,
      state: !ws ? 'Disconnected' :
        ws.readyState === WebSocket.CONNECTING ? 'Connecting...' :
        ws.readyState === WebSocket.CLOSING ? 'Closing...' : 'Disconnected',
      version: BRIDGE_VERSION
    });
  }
  if (message?.type === 'bridge-reconnect') {
    if (ws) {
      try { ws.close(); } catch {}
      ws = undefined;
    }
    ensureConnection();
    return Promise.resolve({ok: true});
  }
});

chrome.runtime.onStartup.addListener(() => { armReconnectAlarm(); ensureConnection(); });
chrome.runtime.onInstalled.addListener(() => { armReconnectAlarm(); ensureConnection(); });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === RECONNECT_ALARM) ensureConnection();
});
chrome.tabs.onUpdated.addListener(() => ensureConnection());
armReconnectAlarm();
connect();
