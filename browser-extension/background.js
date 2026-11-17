const PORT = 17373;
const BRIDGE_VERSION = '0.3.15';
const HEARTBEAT_MS = 20000;
const REQUEST_TIMEOUT_MS = 15000;
const RECONNECT_ALARM = 'cycom-browser-bridge-reconnect';
const DEBUGGER_PROTOCOL = '1.3';
const AGENT_GROUP_TITLE = 'CyCom';
const AGENT_GROUP_COLOR = 'blue';
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
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
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
    let req;
    try { req = JSON.parse(ev.data); } catch { return; }
    if (!req.id) return;
    try {
      const result = await withTimeout(dispatch(req), REQUEST_TIMEOUT_MS, req.action || 'request');
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({id:req.id, ok:true, result}));
    } catch (e) {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({id:req.id, ok:false, error:String(e?.message || e)}));
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
  const point = await exec(t.id, (selector, text, index) => {
    const query = selector || 'a,button,input,textarea,select,[role="button"],[role="link"],[contenteditable="true"],[tabindex]';
    const all = [...document.querySelectorAll(query)].filter((e) => {
      const rect = e.getBoundingClientRect();
      const style = getComputedStyle(e);
      return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
    });
    let e = null;
    if (Number.isInteger(index)) e = all[index] || null;
    if (!e && text) {
      const needle = String(text).trim().toLowerCase();
      e = all.find((x) => {
        const hay = String(x.innerText || x.value || x.getAttribute('aria-label') || x.getAttribute('title') || '').trim().toLowerCase();
        return hay.includes(needle);
      }) || null;
    }
    if (!e && all.length) e = all[0];
    if (!e) throw new Error('element not found');
    e.scrollIntoView({block:'center', inline:'center'});
    const rect = e.getBoundingClientRect();
    return {x: Math.round(rect.left + rect.width / 2), y: Math.round(rect.top + rect.height / 2)};
  }, [r.selector ?? null, r.text ?? null, Number.isInteger(r.index) ? r.index : null]);
  return point;
}

function deepRenderedState() {
  // Bounded DOM walk: never let large dynamic dashboards monopolize the renderer.
  const LIMITS = {text:50000, interactive:180, links:240, headings:120,
                  images:120, tables:32, forms:64, iframes:64, nodes:12000, ms:650};
  const start = performance.now();
  const clean = (value, max=1200) => String(value ?? '').replace(/\s+/g,' ').trim().slice(0,max);
  const attr = (e,key) => { try { return e.getAttribute(key) || ''; } catch { return ''; } };
  const tag = (e) => String(e?.tagName || '').toLowerCase();
  const headings=[], links=[], images=[], tables=[], forms=[], iframes=[], interactive=[], chunks=[];
  let remaining=LIMITS.text, visited=0, truncated=false;
  const root=document.body || document.documentElement;
  if (root) {
    const walker=document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
    let node;
    while ((node=walker.nextNode())) {
      if (++visited > LIMITS.nodes || performance.now()-start > LIMITS.ms) { truncated=true; break; }
      if (node.nodeType === Node.TEXT_NODE) {
        if (remaining <= 0) continue;
        const parent=node.parentElement;
        const ptag=tag(parent);
        if (!parent || ['script','style','noscript','template'].includes(ptag)) continue;
        const str=clean(node.nodeValue,Math.min(remaining,2000));
        if (str) { chunks.push(str); remaining -= str.length+1; }
        continue;
      }
      if (node.nodeType !== Node.ELEMENT_NODE) continue;
      const e=node,t=tag(e),role=attr(e,'role'),type=attr(e,'type').toLowerCase();
      const hidden=e.hasAttribute('hidden') || attr(e,'aria-hidden')==='true';
      if (t==='a' && e.hasAttribute('href') && links.length<LIMITS.links)
        links.push({text:clean(e.textContent || attr(e,'aria-label'),500),href:attr(e,'href'),hidden});
      if (/^h[1-6]$/.test(t) && headings.length<LIMITS.headings)
        headings.push({level:Number(t[1]),text:clean(e.textContent,1000)});
      if (t==='img' && images.length<LIMITS.images)
        images.push({src:attr(e,'src'),alt:clean(attr(e,'alt'),500),width:Number(attr(e,'width'))||0,height:Number(attr(e,'height'))||0,hidden});
      if (t==='table' && tables.length<LIMITS.tables)
        tables.push({caption:clean(e.querySelector('caption')?.textContent,500),text:clean(e.textContent,5000)});
      if (t==='form' && forms.length<LIMITS.forms)
        forms.push({action:attr(e,'action'),method:attr(e,'method'),text:clean(e.textContent,3000)});
      if (t==='iframe' && iframes.length<LIMITS.iframes)
        iframes.push({src:attr(e,'src'),title:attr(e,'title'),name:attr(e,'name'),hidden});
      const isControl=['a','button','input','textarea','select'].includes(t) ||
        ['button','link','checkbox','radio','tab'].includes(role) ||
        attr(e,'contenteditable')==='true' || e.hasAttribute('tabindex');
      if (!isControl || interactive.length>=LIMITS.interactive) continue;
      const password=t==='input' && type==='password';
      let value='';
      if (!password && ['input','textarea','select'].includes(t)) {
        try { value=clean(e.value,1000); } catch {}
      }
      let checked=e.hasAttribute('checked');
      if (t==='input' && ['checkbox','radio'].includes(type)) checked=!!e.checked;
      interactive.push({
        i:interactive.length,tag:t,id:attr(e,'id'),role,
        name:clean(attr(e,'aria-label')||attr(e,'title')||attr(e,'alt')||attr(e,'placeholder')||attr(e,'name')||e.textContent||value,500),
        text:clean(e.textContent||value,800),value:password?'':value,protected:password,type,
        href:attr(e,'href'),disabled:e.hasAttribute('disabled'),checked,
        selected:e.hasAttribute('selected'),editable:['input','textarea','select'].includes(t)||attr(e,'contenteditable')==='true',hidden
      });
    }
  }
  const active=document.activeElement;
  return {
    url:location.href,title:document.title,fullText:clean(chunks.join(' '),LIMITS.text),
    headings,links,images,tables,forms,iframes,interactive,
    viewport:{width:innerWidth,height:innerHeight,scrollX,scrollY},
    selection:clean(globalThis.getSelection?.()?.toString?.(),5000),
    activeElement:active?{tag:tag(active),id:attr(active,'id'),role:attr(active,'role'),name:attr(active,'aria-label')||attr(active,'name')}:null,
    coverage:{textLimit:LIMITS.text,interactiveLimit:LIMITS.interactive,linkLimit:LIMITS.links,
              iframeCount:iframes.length,geometry:'on-demand',domWalkMode:'bounded',visitedNodes:visited,
              truncated,elapsedMs:Math.round(performance.now()-start)}
  };
}

async function captureScreenshot(t) {
  const metrics = await exec(t.id, () => ({
    width: innerWidth,
    height: innerHeight,
    devicePixelRatio: devicePixelRatio || 1
  }));
  const shot = await withDebugger(t.id, async (target) => {
    await chrome.debugger.sendCommand(target, 'Page.enable').catch(() => {});
    return chrome.debugger.sendCommand(target, 'Page.captureScreenshot', {
      format: 'png',
      fromSurface: true,
      captureBeyondViewport: false,
      optimizeForSpeed: true
    });
  });
  if (!shot?.data) throw new Error('Page.captureScreenshot returned no image data');
  return {
    tabId: t.id,
    title: t.title,
    url: t.url,
    mimeType: 'image/png',
    data: shot.data,
    width: Math.round(metrics?.width || 0),
    height: Math.round(metrics?.height || 0),
    pixelWidth: Math.round((metrics?.width || 0) * (metrics?.devicePixelRatio || 1)),
    pixelHeight: Math.round((metrics?.height || 0) * (metrics?.devicePixelRatio || 1)),
    scale: metrics?.devicePixelRatio || 1,
    coordinateSpace: 'css-viewport'
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

function modifierMask(raw=[]) {
  let modifiers = 0;
  for (const part of raw) {
    const u = String(part).trim().toUpperCase();
    if (u === 'ALT' || u === 'OPTION') modifiers |= 1;
    else if (u === 'CTRL' || u === 'CONTROL') modifiers |= 2;
    else if (u === 'META' || u === 'CMD' || u === 'COMMAND' || u === 'SUPER') modifiers |= 4;
    else if (u === 'SHIFT') modifiers |= 8;
  }
  return modifiers;
}

function parseKeypress(r) {
  const raw = Array.isArray(r.keys) && r.keys.length ? r.keys : String(r.key || '').split('+').filter(Boolean);
  if (!raw.length) throw new Error('key or keys is required');
  const modifiers = modifierMask(raw);
  let base = null;
  for (const part of raw) {
    const u = String(part).trim().toUpperCase();
    if (!['ALT','OPTION','CTRL','CONTROL','META','CMD','COMMAND','SUPER','SHIFT'].includes(u)) base = part;
  }
  if (!base) throw new Error('keypress requires a non-modifier key');
  return {modifiers, ...keySpec(base)};
}

async function deepStateViaCDP(tabId) {
  const expression = `(${deepRenderedState.toString()})()`;
  return withDebugger(tabId, async (target) => {
    await chrome.debugger.sendCommand(target, 'Runtime.enable').catch(() => {});
    const evaluated = await withTimeout(
      chrome.debugger.sendCommand(target, 'Runtime.evaluate', {
        expression,
        returnByValue: true,
        awaitPromise: true,
        userGesture: false
      }),
      5000,
      'deep state Runtime.evaluate'
    );
    if (evaluated?.exceptionDetails) {
      const text = evaluated.exceptionDetails.text || evaluated.exceptionDetails.exception?.description || 'Runtime.evaluate failed';
      throw new Error(text);
    }
    const value = evaluated?.result?.value;
    if (!value || typeof value !== 'object') throw new Error('deep state Runtime.evaluate returned no object');
    return value;
  });
}

async function accessibilityState(renderedFrames) {
  const main = renderedFrames?.[0] || {};
  const roleOf = (node) => {
    if (node.role) return node.role;
    const tag = String(node.tag || '').toLowerCase();
    const type = String(node.type || '').toLowerCase();
    if (tag === 'a' && node.href) return 'link';
    if (tag === 'button') return 'button';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'select') return 'combobox';
    if (tag === 'input') {
      if (type === 'checkbox') return 'checkbox';
      if (type === 'radio') return 'radio';
      if (type === 'range') return 'slider';
      if (['button','submit','reset'].includes(type)) return 'button';
      if (type !== 'hidden') return 'textbox';
    }
    return tag || 'generic';
  };
  const nodes = [];
  for (const node of (main.interactive || [])) {
    nodes.push({
      role: roleOf(node),
      name: node.name || node.text || '',
      value: node.protected ? null : (node.value || ''),
      protected: !!node.protected,
      disabled: !!node.disabled,
      checked: node.checked ?? null,
      selected: node.selected ?? null,
      editable: !!node.editable,
      visible: node.visible !== false,
      rect: node.rect || null,
      href: node.href || ''
    });
  }
  for (const h of (main.headings || [])) nodes.push({role:'heading', level:h.level, name:h.text || ''});
  for (const img of (main.images || [])) nodes.push({role:'img', name:img.alt || '', src:img.src || '', visible:img.visible !== false});
  for (const form of (main.forms || [])) nodes.push({role:'form', name:form.text || '', action:form.action || '', method:form.method || ''});
  for (const table of (main.tables || [])) nodes.push({role:'table', name:table.caption || '', rows:table.rows || []});
  const limit = 1000;
  return {
    source:'derived-rendered-state',
    nodeCount:nodes.length,
    returnedNodeCount:Math.min(nodes.length, limit),
    truncated:nodes.length > limit,
    nodes:nodes.slice(0, limit)
  };
}

// Browser Use opens only Agent-owned tabs into a named, reusable Brave Tab Group.
// Querying the existing group also works after a Manifest V3 worker restart.
async function openAgentGroupedTab(r) {
  const groups = await chrome.tabGroups.query({});
  const existing = groups.find(g => g.title === AGENT_GROUP_TITLE && g.color === AGENT_GROUP_COLOR);
  const createInfo = {url:r.url || 'about:blank', active:r.active === true};
  if (existing) createInfo.windowId = existing.windowId;
  const created = await chrome.tabs.create(createInfo);
  try {
    const groupId = await chrome.tabs.group({
      tabIds: [created.id],
      ...(existing ? {groupId:existing.id} : {})
    });
    // Preserve the user's collapsed state for background tabs in an old group.
    const groupProps = {title:AGENT_GROUP_TITLE, color:AGENT_GROUP_COLOR};
    if (!existing || createInfo.active) groupProps.collapsed = false;
    await chrome.tabGroups.update(groupId, groupProps);
    const ready = await waitForTabComplete(created.id);
    return {
      id:created.id, tabId:created.id, groupId, groupTitle:AGENT_GROUP_TITLE,
      title:ready?.title || '', url:ready?.url || createInfo.url,
      active:ready?.active ?? createInfo.active
    };
  } catch (error) {
    // If grouping fails, don't strand an Agent tab among personal tabs.
    await chrome.tabs.remove(created.id).catch(() => {});
    throw new Error(`Unable to group CyCom tab: ${String(error?.message || error)}`);
  }
}

async function dispatch(r) {
  if (r.action === 'tabs') {
    return chrome.tabs.query({}).then(xs => xs.map(t => ({
      id:t.id, title:t.title, url:t.url, active:t.active, windowId:t.windowId,
      groupId:t.groupId, bridgeVersion:BRIDGE_VERSION
    })));
  }
  if (r.action === 'open') {
    return openAgentGroupedTab(r);
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
    let main;
    try {
      main = await withTimeout(exec(t.id, deepRenderedState, [], false), 4500, "DOM state extraction");
    } catch (domError) {
      main = await withTimeout(deepStateViaCDP(t.id), 6500, "CDP state fallback");
    }
    if (!main || typeof main !== 'object') throw new Error('main-frame state unavailable');
    const renderedFrames = [{frameId:0, documentId:'', ...main}];
    const accessibility = await accessibilityState(renderedFrames);
    return {
      tabId:t.id,
      title:t.title,
      url:t.url,
      bridgeVersion:BRIDGE_VERSION,
      frameCount:1,
      ...main,
      frames:renderedFrames,
      accessibility
    };
  }
  if (r.action === 'screenshot') return captureScreenshot(t);
  if (r.action === 'wait') {
    await new Promise(resolve => setTimeout(resolve, Math.max(0, Math.min(Number(r.ms) || 1000, 15000))));
    return {tabId:t.id, waitedMs:Math.max(0, Math.min(Number(r.ms) || 1000, 15000))};
  }

  // Background automation must not steal the user's active tab/window.
  // Only explicitly requested foreground actions may switch focus.
  if (r.focus === true && ['move','click','double_click','drag','scroll','type','keypress'].includes(r.action)) {
    await focusTab(t);
  }

  // Chrome CDP Input can acknowledge events on inactive tabs without delivering
  // them.  For common semantic tasks, use bounded DOM interaction instead of
  // claiming success, while keeping the user's current tab untouched.
  if (!t.active && r.focus !== true && ['click','double_click','type','scroll','move','drag','keypress'].includes(r.action)) {
    if (!['click','double_click','type','scroll'].includes(r.action)) {
      throw new Error(`Background ${r.action} needs an active tab; explicitly switch/focus to use real CDP input`);
    }
    const result = await exec(t.id, (request) => {
      const action = request.action;
      const query = request.selector || 'a[href],button,input,textarea,select,[role="button"],[role="link"],[contenteditable="true"],[tabindex]';
      const pick = () => {
        if (Number.isFinite(request.x) && Number.isFinite(request.y)) return document.elementFromPoint(request.x,request.y);
        const list = [...document.querySelectorAll(query)];
        if (Number.isInteger(request.index)) return list[request.index] || null;
        if (request.selector) return list[0] || null;
        if (action === 'type') {
          const focused = document.activeElement;
          if (focused && focused !== document.body) return focused;
        }
        if (request.text && action !== 'type') {
          const needle = String(request.text).trim().toLowerCase();
          return list.find(el => String(el.textContent || el.value || el.getAttribute('aria-label') || '').toLowerCase().includes(needle)) || null;
        }
        return list[0] || null;
      };
      if (action === 'scroll') {
        const dx=Number(request.deltaX ?? request.xDelta ?? request.x ?? 0) || 0;
        const dy=Number(request.deltaY ?? request.yDelta ?? request.y ?? 600) || 0;
        scrollBy(dx,dy);
        return {scrollX,scrollY,deltaX:dx,deltaY:dy};
      }
      const el=pick();
      if (!el) throw new Error('background target element not found');
      if (action === 'click' || action === 'double_click') {
        if (request.button && request.button !== 'left' && request.button !== 1) throw new Error('background click supports left button only');
        if (!(el instanceof HTMLElement) && !(el instanceof SVGElement)) throw new Error('target is not clickable');
        el.scrollIntoView({block:'nearest',inline:'nearest'});
        el.focus?.({preventScroll:true});
        el.click();
        if (action === 'double_click') {
          el.click();
          el.dispatchEvent(new MouseEvent('dblclick',{bubbles:true,cancelable:true,view:window}));
        }
        return {tag:el.tagName,id:el.id || '',clicked:true,synthetic:true};
      }
      // Native setter + input/change events make simple inputs work with
      // frameworks while avoiding synthetic typing into the user's active tab.
      if (action === 'type') {
        const text=String(request.text ?? '');
        const tag=el.tagName?.toLowerCase();
        if (tag !== 'input' && tag !== 'textarea') throw new Error('background typing supports input/textarea only; use explicit focus for rich editors');
        if (el.disabled || el.readOnly) throw new Error('input is disabled or readonly');
        const old=String(el.value || '');
        el.focus({preventScroll:true});
        let start=old.length,end=old.length;
        try { start=Number.isInteger(el.selectionStart)?el.selectionStart:old.length;end=Number.isInteger(el.selectionEnd)?el.selectionEnd:start; } catch {}
        const next=old.slice(0,start)+text+old.slice(end);
        const proto=tag==='textarea'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
        const setter=Object.getOwnPropertyDescriptor(proto,'value')?.set;
        if (!setter) throw new Error('input setter is unavailable');
        setter.call(el,next);
        try { el.setSelectionRange(start+text.length,start+text.length); } catch {}
        el.dispatchEvent(new InputEvent('input',{bubbles:true,cancelable:true,data:text,inputType:'insertText'}));
        el.dispatchEvent(new Event('change',{bubbles:true}));
        if (String(el.value) !== next) throw new Error('background input was rejected or reset by page');
        return {tag,id:el.id || '',typed:text.length,synthetic:true};
      }
      throw new Error('unsupported background action');
    }, [r]);
    return {tabId:t.id, mode:'background-dom', ...result};
  }

  if (r.action === 'move') {
    const p = await resolvePoint(t, r);
    const modifiers = modifierMask(Array.isArray(r.keys) ? r.keys : []);
    return withDebugger(t.id, async (target) => {
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y, modifiers});
      return {tabId:t.id, x:p.x, y:p.y, modifiers};
    });
  }

  if (r.action === 'click' || r.action === 'double_click') {
    const p = await resolvePoint(t, r);
    const button = mouseButton(r.button);
    const count = r.action === 'double_click' ? 2 : 1;
    const modifiers = modifierMask(Array.isArray(r.keys) ? r.keys : []);
    return withDebugger(t.id, async (target) => {
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y, modifiers});
      for (let i = 1; i <= count; i++) {
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:p.x, y:p.y, button, clickCount:i, modifiers});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:p.x, y:p.y, button, clickCount:i, modifiers});
      }
      return {tabId:t.id, x:p.x, y:p.y, button, clickCount:count, modifiers};
    });
  }

  if (r.action === 'drag') {
    let path = Array.isArray(r.path) ? r.path.filter(p => Number.isFinite(p?.x) && Number.isFinite(p?.y)) : [];
    if (path.length < 2 && [r.startX,r.startY,r.endX,r.endY].every(Number.isFinite)) {
      path = [{x:r.startX,y:r.startY},{x:r.endX,y:r.endY}];
    }
    if (path.length < 2) throw new Error('drag requires path with at least 2 points or start/end coordinates');
    const button = mouseButton(r.button);
    const modifiers = modifierMask(Array.isArray(r.keys) ? r.keys : []);
    return withDebugger(t.id, async (target) => {
      const first = path[0];
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:first.x, y:first.y, modifiers});
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:first.x, y:first.y, button, clickCount:1, modifiers});
      for (const p of path.slice(1)) {
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y, button, buttons:1, modifiers});
      }
      const last = path[path.length - 1];
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:last.x, y:last.y, button, clickCount:1, modifiers});
      return {tabId:t.id, button, path, modifiers};
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
    const modifiers = modifierMask(Array.isArray(r.keys) ? r.keys : []);
    return withDebugger(t.id, async (target) => {
      await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {
        type:'mouseWheel', x:p.x, y:p.y, deltaX, deltaY, modifiers
      });
      return {tabId:t.id, x:p.x, y:p.y, deltaX, deltaY, modifiers};
    });
  }

  if (r.action === 'type') {
    if (r.selector || Number.isInteger(r.index) || (Number.isFinite(r.x) && Number.isFinite(r.y))) {
      const p = await resolvePoint(t, r);
      await withDebugger(t.id, async (target) => {
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseMoved', x:p.x, y:p.y});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mousePressed', x:p.x, y:p.y, button:'left', clickCount:1});
        await chrome.debugger.sendCommand(target, 'Input.dispatchMouseEvent', {type:'mouseReleased', x:p.x, y:p.y, button:'left', clickCount:1});
      });
    }
    return withDebugger(t.id, async (target) => {
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
