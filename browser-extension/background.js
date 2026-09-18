const PORT = 17373;
const HEARTBEAT_MS = 20000;
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

function connect() {
  clearTimeout(retry);
  retry = undefined;
  if (ws && ws.readyState !== WebSocket.CLOSED) return;
  const socket = new WebSocket(`ws://127.0.0.1:${PORT}/extension`);
  ws = socket;
  socket.onopen = () => {
    if (ws !== socket) return socket.close();
    socket.send(JSON.stringify({type:'hello', version:'0.1.0'}));
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
    try { socket.send(JSON.stringify({id:req.id, ok:true, result:await dispatch(req)})); }
    catch (e) { socket.send(JSON.stringify({id:req.id, ok:false, error:String(e?.message || e)})); }
  };
}

function ensureConnection() {
  if (ws && ws.readyState !== WebSocket.CLOSED) return;
  connect();
}

async function tab(tabId) {
  if (tabId) return chrome.tabs.get(tabId);
  const [t] = await chrome.tabs.query({active:true, lastFocusedWindow:true});
  if (!t) throw new Error('No active tab');
  return t;
}

async function exec(tabId, func, args=[]) {
  const r = await chrome.scripting.executeScript({target:{tabId}, func, args});
  return r[0]?.result;
}

async function dispatch(r) {
  if (r.action === 'tabs') return chrome.tabs.query({}).then(xs=>xs.map(t=>({id:t.id,title:t.title,url:t.url,active:t.active,windowId:t.windowId})));
  if (r.action === 'open') { const t=await chrome.tabs.create({url:r.url,active:r.active!==false}); return {id:t.id,title:t.title,url:t.url}; }
  const t = await tab(r.tabId);
  if (r.action === 'navigate') { await chrome.tabs.update(t.id,{url:r.url}); return {tabId:t.id}; }
  if (r.action === 'state') return exec(t.id, () => ({
    url:location.href,title:document.title,
    elements:[...document.querySelectorAll('a,button,input,textarea,select,[role="button"],[contenteditable="true"]')].slice(0,500).map((e,i)=>({i,tag:e.tagName.toLowerCase(),text:(e.innerText||e.value||e.getAttribute('aria-label')||e.title||'').trim().slice(0,300),type:e.type||'',href:e.href||'',disabled:!!e.disabled,rect:(()=>{const x=e.getBoundingClientRect();return {x:x.x,y:x.y,w:x.width,h:x.height}})()}))
  }));
  if (r.action === 'click') return exec(t.id, (q,text,index) => { const all=[...document.querySelectorAll(q||'a,button,[role="button"]')]; const e=index!=null?all[index]:all.find(x=>(x.innerText||x.value||x.getAttribute('aria-label')||'').trim().includes(text||'')); if(!e) throw Error('element not found'); e.scrollIntoView({block:'center'}); e.click(); return true; }, [r.selector,r.text,r.index]);
  if (r.action === 'type') return exec(t.id, (q,text,index) => { const all=[...document.querySelectorAll(q||'input,textarea,[contenteditable="true"]')]; const e=index!=null?all[index]:all[0]; if(!e) throw Error('element not found'); e.focus(); if('value' in e){ const s=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e),'value')?.set; s?s.call(e,text):e.value=text; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); } else {e.textContent=text;e.dispatchEvent(new InputEvent('input',{bubbles:true,data:text,inputType:'insertText'}));} return true; }, [r.selector,r.text,r.index]);
  if (r.action === 'scroll') return exec(t.id,(x,y)=>{scrollBy(x,y);return {x:scrollX,y:scrollY}},[r.x||0,r.y||600]);
  throw new Error(`Unknown action: ${r.action}`);
}

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
chrome.tabs.onUpdated.addListener(() => ensureConnection());
connect();
