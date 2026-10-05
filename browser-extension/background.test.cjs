const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto');

const source = fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8');
const listener = {addListener() {}};
function fixture() {
  const chrome = {
    debugger:{onDetach:listener},
    tabs:{onRemoved:listener, onUpdated:listener},
    runtime:{onMessage:listener, onStartup:listener, onInstalled:listener},
    alarms:{create() {}, onAlarm:listener}
  };
  class WebSocket {
    static CONNECTING=0; static OPEN=1; static CLOSED=3; readyState=0;
    close(code, reason) { this.closed={code, reason}; }
  }
  const document = {
    nodes:[], title:'Fixture', body:{innerText:'Fixture'}, documentElement:{}, activeElement:null,
    createTreeWalker(root) { let i=0; return {nextNode:() => root.nodes[i++] || null}; },
    elementFromPoint(x) { return this.overlay || this.nodes.find(e => e.isConnected && e.rect.left <= x && e.rect.right >= x); }
  };
  const context = vm.createContext({
    chrome, WebSocket, document, NodeFilter:{SHOW_ELEMENT:1}, crypto,
    location:{href:'https://fixture.invalid'}, innerWidth:1000, innerHeight:800, scrollX:0, scrollY:0,
    getSelection:() => null, getComputedStyle:e => e.style,
    TextEncoder, setTimeout:() => 1, clearTimeout() {}, setInterval:() => 1, clearInterval() {}
  });
  vm.runInContext(source, context);
  function element(tag, label, x, opts={}) {
    const e = {
      tagName:tag, innerText:label, textContent:label, id:opts.id || label, isConnected:true,
      parentElement:null, getRootNode:() => document,
      attrs:opts.attrs || {}, getAttribute(key) { return this.attrs[key] ?? null; },
      style:{visibility:'visible', display:'block', opacity:'1', pointerEvents:'auto', ...opts.style},
      rect:{x, y:10, left:x, right:x+40, top:10, bottom:30, width:40, height:20},
      getBoundingClientRect() { return this.rect; }, scrollIntoView() {},
      matches(selector) {
        if (selector.startsWith('#')) return this.id === selector.slice(1);
        return tag === 'BUTTON' || ['checkbox','radio','tab'].includes(this.attrs.role);
      },
      ...opts
    };
    document.nodes.push(e);
    return e;
  }
  const state = () => vm.runInContext('browserPage()', context);
  function resolve(target) { context.target=target; return vm.runInContext("browserPage('resolve', target)", context); }
  return {context, document, element, state, resolve};
}

test('manifest packages every referenced file and endpoint matches Go clients/server', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, 'manifest.json')));
  const refs = [manifest.background.service_worker, manifest.action?.default_popup,
    ...Object.values(manifest.icons || {}), ...Object.values(manifest.action?.default_icon || {})].filter(Boolean);
  for (const file of refs) assert.ok(fs.existsSync(path.join(__dirname, file)), `missing manifest file: ${file}`);
  const port = /const PORT = (\d+)/.exec(source)[1];
  assert.match(manifest.content_security_policy.extension_pages, new RegExp(`ws://127\\.0\\.0\\.1:${port}`));
  const contract = fs.readFileSync(path.join(__dirname, '../internal/browserbridge/contract.go'), 'utf8');
  assert.match(contract, new RegExp(`127\\.0\\.0\\.1:${port}`));
});

test('indexed targets use the visible observed set and survive DOM reordering', () => {
  const f=fixture();
  f.element('BUTTON', 'Hidden', 0, {style:{visibility:'hidden', display:'block', opacity:'1'}});
  f.element('DIV', 'Checkbox', 100, {attrs:{role:'checkbox'}});
  const submit=f.element('BUTTON', 'Submit', 200);
  const s=f.state();
  assert.deepEqual(Array.from(s.interactive, e => e.name), ['Checkbox','Submit']);
  const point=f.resolve({index:1, snapshotId:s.snapshotId});
  assert.equal(point.x, 220);
  f.document.nodes.reverse();
  assert.equal(f.resolve({index:1, snapshotId:s.snapshotId}).x, 220);
  submit.isConnected=false;
  assert.throws(() => f.resolve({index:1, snapshotId:s.snapshotId}), /missing or hidden/);
});

test('stale, absent, out of range, hidden, and ambiguous targets fail closed', () => {
  const f=fixture();
  const first=f.element('BUTTON', 'Save', 100);
  f.element('BUTTON', 'Save', 200);
  const s=f.state();
  assert.throws(() => f.resolve({index:0}), /snapshot/);
  assert.throws(() => f.resolve({index:100, snapshotId:s.snapshotId, text:'Save'}), /missing/);
  assert.throws(() => f.resolve({text:'Missing'}), /not found/);
  assert.throws(() => f.resolve({selector:'#missing'}), /not found/);
  assert.throws(() => f.resolve({text:'Save'}), /ambiguous/);
  assert.throws(() => f.resolve({}), /required/);
  first.style.visibility='hidden';
  assert.throws(() => f.resolve({index:0, snapshotId:s.snapshotId}), /hidden/);
  f.state();
  assert.throws(() => f.resolve({index:1, snapshotId:s.snapshotId}), /stale/);
});

test('disabled and obscured targets fail before pointer input', () => {
  const f=fixture();
  const button=f.element('BUTTON','Submit',100);
  const s=f.state();
  button.disabled=true;
  assert.throws(() => f.resolve({index:0, snapshotId:s.snapshotId}), /disabled/);
  button.disabled=false;
  f.document.overlay=f.element('DIV','Overlay',100);
  assert.throws(() => f.resolve({index:0, snapshotId:s.snapshotId}), /obscured/);
});

test('an observed element with changed meaning is rejected', () => {
  const f=fixture();
  const button=f.element('BUTTON','Preview',100);
  const s=f.state();
  button.innerText='Submit payment';
  assert.throws(() => f.resolve({index:0, snapshotId:s.snapshotId}), /changed/);
});

test('snapshot identities work on ordinary HTTP pages without randomUUID', () => {
  const f=fixture();
  f.context.crypto={getRandomValues:crypto.getRandomValues.bind(crypto)};
  f.element('BUTTON','Submit',100);
  const s=f.state();
  assert.match(s.snapshotId,/^[0-9a-f]{32}$/);
  assert.equal(f.resolve({index:0, snapshotId:s.snapshotId}).x,120);
});

test('open shadow DOM targets share state identity and hit testing', () => {
  const f=fixture();
  const host=f.element('DIV','Host',100);
  const button=f.element('BUTTON','Shadow submit',100);
  f.document.nodes.pop();
  const root={host, nodes:[button], elementFromPoint:() => button};
  button.getRootNode=() => root;
  host.shadowRoot=root;
  const s=f.state();
  assert.equal(s.interactive[0].name,'Shadow submit');
  assert.equal(f.resolve({index:0, snapshotId:s.snapshotId}).x,120);
});

test('child frame semantic targets are rejected before any browser API call', async () => {
  const f=fixture();
  await assert.rejects(vm.runInContext('resolvePoint({id:1}, {frameId:2, index:0, snapshotId:"frame"})', f.context), /top frame/);
});

test('missing click and type targets never dispatch pointer or text input', async () => {
  const f=fixture();
  f.element('BUTTON','Submit',100);
  const commands=[];
  f.context.chrome.tabs.get=async id => ({id, windowId:1});
  f.context.chrome.tabs.update=async () => {};
  f.context.chrome.windows={update:async () => {}};
  f.context.chrome.debugger.sendCommand=async (target, name, params) => { commands.push({name,params}); };
  f.context.chrome.scripting={executeScript:async opts => [{result:opts.func(...opts.args)}]};
  await assert.rejects(vm.runInContext('dispatch({action:"click",tabId:1,text:"Missing"})', f.context), /not found/);
  await assert.rejects(vm.runInContext('dispatch({action:"type",tabId:1,index:"bad",text:"secret"})', f.context), /integer/);
  assert.equal(commands.filter(command => command.name.startsWith('Input.')).length,0);
});

test('oversized extension response becomes bounded error envelope', () => {
  const f=fixture();
  const sent=[];
  f.context.socket={readyState:1, send:payload => sent.push(payload)};
  f.context.response={id:'test',ok:true,result:'x'.repeat((16 << 20)+1)};
  vm.runInContext('sendResponse(socket, response)', f.context);
  const result=JSON.parse(sent[0]);
  assert.equal(result.ok,false);
  assert.match(result.error,/16 MiB/);
  assert.ok(Buffer.byteLength(sent[0]) < 1024);
});

test('oversized incoming request closes the socket before dispatch', async () => {
  const f=fixture();
  const socket=vm.runInContext('ws', f.context);
  await socket.onmessage({data:JSON.stringify({id:'large',action:'open',url:'x'.repeat(1 << 20)})});
  assert.equal(socket.closed.code,1009);
});
