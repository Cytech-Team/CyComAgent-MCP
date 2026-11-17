(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const state = {events:[], active:[], healing:null, online:false, paused:false, search:'', filter:'all', tool:'all', range:'all', sort:'newest', visible:25, selected:null, lastPoll:0, lastEventKey:'', view:'overview', fetching:false};
  const format = new Intl.NumberFormat('en-US');
  const timeFormat = new Intl.DateTimeFormat(undefined, {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  const dateFormat = new Intl.DateTimeFormat(undefined, {day:'2-digit',month:'short',year:'numeric'});
  const dayFormat = new Intl.DateTimeFormat(undefined, {weekday:'short'});
  const numeric = n => format.format(Number(n)||0);
  const safeDate = v => {const d = new Date(v);return Number.isNaN(d.getTime())?null:d;};
  const relative = date => {if (!date)return 'Unknown time';const seconds = Math.max(0,Math.floor((Date.now()-date.getTime())/1000));if(seconds<5)return 'just now';if(seconds<60)return `${seconds}s ago`;if(seconds<3600)return `${Math.floor(seconds/60)}m ago`;if(seconds<86400)return `${Math.floor(seconds/3600)}h ago`;return `${Math.floor(seconds/86400)}d ago`;};
  const node = (tag,className,text) => {const el=document.createElement(tag);if(className)el.className=className;if(text!==undefined)el.textContent=String(text);return el;};
  const set = (id,text) => {const el=$(id);if(el)el.textContent=text;};
  const toolLabel = t => String(t||'unknown');
  const parseTime = e => safeDate(e.time)?.getTime() || 0;
  const timeText = e => {const dt=safeDate(e.time);return dt ? timeFormat.format(dt) : '—';};
  const dateText = e => {const dt=safeDate(e.time);return dt ? dateFormat.format(dt) : 'Unknown date';};
  const duration = e => {const ms=Number(e.duration_ms);return Number.isFinite(ms) ? `${numeric(Math.max(0, Math.round(ms)))} ms` : '—';};
  function connection(online) {
    state.online=online;
    const chip=$('connection-chip');
    chip.classList.toggle('offline',!online);chip.classList.toggle('paused',online&&state.paused);
    set('connection-text',!online?'Disconnected':state.paused?'Paused':'Connected');
    set('sidebar-stream',!online?'Offline':state.paused?'Paused':'Connected');
    set('system-connection',!online?'Disconnected':state.paused?'Connected · paused':'Healthy');
    $('system-connection').style.color=online?'var(--green)':'var(--red)';
    document.querySelectorAll('.signal-pulse').forEach(el=>el.style.background=online?'':'var(--red)');
    document.querySelectorAll('.context-led').forEach(el=>el.style.background=online?'var(--green)':'var(--red)');
  }
  function filtered() {
    const now=Date.now();const cutoff=state.range==='24h'?now-86400000:state.range==='7d'?now-604800000:0;
    const query=state.search.trim().toLowerCase();
    return state.events.filter(e=>{
      if(state.filter==='success'&&!e.ok)return false;
      if(state.filter==='failed'&&e.ok)return false;
      if(state.tool!=='all'&&toolLabel(e.tool)!==state.tool)return false;
      if(cutoff&&parseTime(e)<cutoff)return false;
      if(query&&!`${e.tool||''} ${e.reason||''} ${e.error||''} ${e.args_sha256||''} ${e.trace_id||''} ${e.call_id||''}`.toLowerCase().includes(query))return false;
      return true;
    }).sort((a,b)=>state.sort==='oldest'?parseTime(a)-parseTime(b):state.sort==='slowest'?(Number(b.duration_ms)||0)-(Number(a.duration_ms)||0):parseTime(b)-parseTime(a));
  }
  function refreshStats() {
    const all=state.events;const total=all.length;const good=all.filter(x=>x.ok).length;const bad=total-good;
    const valid=all.map(x=>Number(x.duration_ms)).filter(x=>Number.isFinite(x)&&x>=0);
    set('metric-total',numeric(total));set('metric-success',numeric(good));set('metric-failed',numeric(bad));
    set('metric-latency-value',valid.length?numeric(Math.round(valid.reduce((a,b)=>a+b,0)/valid.length)):'—');
    set('success-pct',total?(100*good/total).toFixed(1)+'%':'—');set('failure-pct',total?(100*bad/total).toFixed(1)+'%':'—');
    set('nav-count',numeric(total));set('nav-failed',numeric(bad));
    set('system-running',numeric(state.active.length));
    const latest=all[0];set('system-last-event',latest?relative(safeDate(latest.time)):'No events');
    set('system-last-detail',latest ? `${toolLabel(latest.tool)} · ${dateText(latest)}` : 'No audit records available');
    const healing=state.healing||{};
    set('system-healing',String(healing.status||healing.action||'No activity').replace(/_/g,' '));
    set('system-healing-detail',healing.component?`${healing.component} · ${healing.reason||''}`:String(healing.event||'No self-healing event recorded'));
  }
  function renderChart() {
    const holder=$('activity-chart');holder.replaceChildren();
    const days=[];const now=new Date();
    for(let i=6;i>=0;i--){const start=new Date(now.getFullYear(),now.getMonth(),now.getDate()-i);const end=new Date(start);end.setDate(end.getDate()+1);days.push({start:start.getTime(),end:end.getTime(),date:start,success:0,failed:0});}
    state.events.forEach(e=>{const ts=parseTime(e);const day=days.find(d=>ts>=d.start&&ts<d.end);if(day)day[e.ok?'success':'failed']++;});
    const max=Math.max(1,...days.map(d=>d.success+d.failed));
    for(const day of days){
      const wrap=node('div','chart-day');const barWrap=node('div','chart-bars');const sum=day.success+day.failed;
      const failed=node('div','chart-bar fail');const good=node('div','chart-bar success');
      if(sum===0){good.classList.add('zero');good.style.height='2px';}else{
        failed.style.height=`${day.failed/max*100}%`;good.style.height=`${Math.max(1,day.success/max*100)}%`;
      }
      barWrap.append(failed,good);const date=node('span','chart-day-label',dayFormat.format(day.date));
      wrap.title=`${dateFormat.format(day.date)} · ${sum} calls · ${day.success} success · ${day.failed} failed`;
      wrap.append(barWrap,date);holder.append(wrap);
    }
  }
  function syncToolOptions() {
    const select=$('tool-filter');if(!select)return;
    const current=state.tool;const names=[...new Set(state.events.map(e=>toolLabel(e.tool)))].sort((a,b)=>a.localeCompare(b));
    select.replaceChildren(new Option('All tools','all'));
    for(const name of names)select.add(new Option(name,name));
    select.value=names.includes(current)?current:'all';state.tool=select.value;
  }
  function renderTopTools() {
    const holder=$('top-tools');holder.replaceChildren();
    const counts=new Map();for(const e of state.events){const key=toolLabel(e.tool);counts.set(key,(counts.get(key)||0)+1);}
    const entries=[...counts.entries()].sort((a,b)=>b[1]-a[1]).slice(0,5);
    if(!entries.length){holder.append(node('div','chart-placeholder','No tool activity yet'));return;}
    const max=entries[0][1];entries.forEach(([name,count],idx)=>{
      const row=node('div','tool-rank');const rank=node('span','tool-rank-number',String(idx+1).padStart(2,'0'));
      const main=node('div','tool-rank-main');const label=node('div','tool-rank-label');
      const title=node('span','tool-rank-name',name);title.title=name;label.append(title,node('span','tool-rank-count',numeric(count)));
      const track=node('div','tool-rank-track');const fill=node('div','tool-rank-fill');fill.style.width=`${count/max*100}%`;track.append(fill);main.append(label,track);row.append(rank,main);holder.append(row);
    });
  }
  const eventKey = e => `${e.time||''}|${e.tool||''}|${e.args_sha256||''}|${e.error||''}`;
  function renderRows() {
    const all=filtered();const rows=all.slice(0,state.visible);set('filtered-count',numeric(all.length));
    set('showing-label',`${numeric(all.length)} matching`);set('footer-count',`Showing ${numeric(rows.length)} of ${numeric(all.length)} matching · ${numeric(state.events.length)} sampled`);
    const more=$('more-btn');more.hidden=rows.length>=all.length;
    const tbody=$('event-rows');const fragment=document.createDocumentFragment();
    if(!rows.length){const tr=node('tr');const td=node('td','empty-row',state.events.length?'No calls match your filters. Try changing the search or time range.':'No audit calls recorded yet.');td.colSpan=6;tr.append(td);fragment.append(tr);}
    for(const e of rows){
      const tr=node('tr','event-row');tr.tabIndex=0;tr.setAttribute('aria-label',`${toolLabel(e.tool)}, ${e.ok?'successful':'failed'}, ${timeText(e)}. Open details`);
      const time=node('td');time.append(node('div','time-primary',timeText(e)),node('div','time-secondary',dateText(e)));
      const tool=node('td');tool.append(node('span','tool-pill',toolLabel(e.tool)));
      const reason=node('td');const reasonText=e.reason||'Legacy call — no reason recorded';const reasonDiv=node('div','reason-cell',reasonText);reasonDiv.title=reasonText;reason.append(reasonDiv);
      const elapsed=node('td','duration-cell',duration(e));if(Number(e.duration_ms)>5000)elapsed.classList.add('long');
      const status=node('td');status.append(node('span',`status-pill ${e.ok?'success':'failed'}`,e.ok?'Success':'Failed'));
      const arrow=node('td','', '›');tr.append(time,tool,reason,elapsed,status,arrow);
      tr.addEventListener('click',()=>openDetails(e));tr.addEventListener('keydown',ev=>{if(ev.key==='Enter'||ev.key===' '){ev.preventDefault();openDetails(e);}});
      fragment.append(tr);
    }
    tbody.replaceChildren(fragment);
  }
  function render() {refreshStats();renderChart();renderTopTools();syncToolOptions();renderRows();}
  function syncText(){const t=state.lastPoll?relative(new Date(state.lastPoll)):'never';const latest=state.events[0];const callAge=latest?` · newest event ${relative(safeDate(latest.time))}`:'';set('sync-message',state.online?(state.paused?`Updates paused · last fetched ${t}`:`Connected · last synced ${t}${callAge}`):'Connection lost · retrying automatically');set('system-last-poll',state.lastPoll?`Last successful response ${t}`:'Awaiting first response');}
  async function refresh(manual=false){
    if(state.fetching||(!manual&&state.paused))return;
    state.fetching=true;
    const controller=new AbortController();const timeout=setTimeout(()=>controller.abort(),7000);
    try{
      const response=await fetch('/inspector/events',{cache:'no-store',signal:controller.signal,credentials:'same-origin'});
      if(!response.ok)throw new Error(`HTTP ${response.status}`);
      const payload=await response.json();if(!Array.isArray(payload.events))throw new Error('Invalid audit stream response');
      state.events=payload.events.filter(v=>v&&typeof v==='object');state.active=Array.isArray(payload.active)?payload.active:[];state.healing=payload.self_healing||null;
      state.lastPoll=Date.now();const key=state.events.map(eventKey).join('~');
      if(key!==state.lastEventKey||manual){state.lastEventKey=key;render();}else{refreshStats();}
      connection(true);syncText();
      if(manual)toast('Audit stream refreshed');
    }catch(err){connection(false);syncText();if(manual)toast(`Unable to refresh: ${err.message}`);}
    finally{clearTimeout(timeout);state.fetching=false;}
  }
  function view(name){
    state.view=name;
    document.querySelectorAll('.nav-link').forEach(btn=>{const chosen=btn.dataset.view===name;btn.classList.toggle('selected',chosen);if(chosen)btn.setAttribute('aria-current','page');else btn.removeAttribute('aria-current');});
    const showSystem=name==='system';$('system-panel').hidden=!showSystem;
    if(name==='failures'){state.filter='failed';syncFilter();renderRows();$('activity-section').scrollIntoView({block:'start',behavior:'smooth'});}
    if(name==='activity'){state.filter='all';syncFilter();renderRows();}
    if(name==='activity'){$('activity-section').scrollIntoView({block:'start',behavior:'smooth'});}
    if(name==='overview'){window.scrollTo({top:0,behavior:'smooth'});}
    if(name==='system'){$('system-panel').scrollIntoView({block:'start',behavior:'smooth'});}
    $('sidebar').classList.remove('open');
  }
  function syncFilter(){document.querySelectorAll('[data-filter]').forEach(btn=>{const active=btn.dataset.filter===state.filter;btn.classList.toggle('active',active);btn.setAttribute('aria-pressed',String(active));});}
  function kv(label,value,cls=''){const wrap=node('div','drawer-section');wrap.append(node('div','drawer-section-label',label),node('div',`drawer-value ${cls}`,value));return wrap;}
  function openDetails(e){state.selected=e;const holder=$('drawer-body');holder.replaceChildren();
    const status=node('div','drawer-section');status.append(node('div','drawer-section-label','EXECUTION STATUS'),node('div',`status-pill drawer-status ${e.ok?'success':'failed'}`,e.ok?'Successful execution':'Failed execution'));holder.append(status);
    holder.append(kv('TOOL',toolLabel(e.tool),'mono'));
    if(e.trace_id)holder.append(kv('TRACE ID',e.trace_id,'mono'));
    if(e.call_id)holder.append(kv('CALL ID',e.call_id,'mono'));
    const grid=node('div','drawer-section drawer-grid');
    const a=node('div');a.append(node('div','drawer-section-label','STARTED AT'),node('strong','',`${dateText(e)} · ${timeText(e)}`));
    const b=node('div');b.append(node('div','drawer-section-label','DURATION'),node('strong','',duration(e)));
    grid.append(a,b);holder.append(grid);
    holder.append(kv('EXECUTION REASON',e.reason||'Legacy call — no reason was recorded','drawer-reason'));
    if(e.error)holder.append(kv('ERROR MESSAGE',e.error,'drawer-reason'));
    holder.append(kv('ARGUMENTS FINGERPRINT',e.args_sha256||'Not available','mono'));
    holder.append(kv('AUDIT TIMESTAMP',e.time||'Not available','mono'));
    $('detail-drawer').hidden=false;$('drawer-backdrop').hidden=false;$('drawer-close').focus();document.body.style.overflow='hidden';
  }
  function closeDetails(){$('detail-drawer').hidden=true;$('drawer-backdrop').hidden=true;document.body.style.overflow='';state.selected=null;}
  let toastTimer;function toast(message){const box=$('toast');box.textContent=message;box.classList.add('visible');clearTimeout(toastTimer);toastTimer=setTimeout(()=>box.classList.remove('visible'),2600);}
  function csvValue(v){const value=String(v??'');const safe=/^\s*[=+@-]/.test(value)?'\''+value:value;return '"'+safe.replace(/"/g,'""')+'"';}
  function exportCSV(){const rows=filtered();if(!rows.length){toast('No matching events to export');return;}
    const header=['Timestamp','Trace ID','Call ID','Tool','Status','Duration (ms)','Reason','Error','Args SHA256'];
    const data=[header.map(csvValue).join(','),...rows.map(e=>[e.time,e.trace_id,e.call_id,e.tool,e.ok?'Success':'Failed',e.duration_ms,e.reason,e.error,e.args_sha256].map(csvValue).join(','))].join('\r\n');
    const file=new Blob(['\ufeff',data],{type:'text/csv;charset=utf-8'});const url=URL.createObjectURL(file);
    const link=document.createElement('a');link.href=url;link.download=`cycom-audit-${new Date().toISOString().slice(0,10)}.csv`;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);toast(`Exported ${rows.length} events`);
  }
  function setup(){
    document.querySelectorAll('.nav-link').forEach(btn=>btn.addEventListener('click',()=>view(btn.dataset.view)));
    $('mobile-menu').addEventListener('click',()=>$('sidebar').classList.toggle('open'));
    $('refresh-btn').addEventListener('click',()=>refresh(true));$('export-btn').addEventListener('click',exportCSV);
    $('pause-btn').addEventListener('click',()=>{state.paused=!state.paused;const btn=$('pause-btn');btn.querySelector('span').textContent=state.paused?'Resume live':'Pause live';btn.querySelector('svg').innerHTML=state.paused?'<path d="m8 5 11 7-11 7V5Z"/>':'<path d="M8 5v14M16 5v14"/>';connection(state.online);syncText();if(!state.paused)refresh(true);});
    $('search-input').addEventListener('input',e=>{state.search=e.target.value;state.visible=25;renderRows();});
    $('range-filter').addEventListener('change',e=>{state.range=e.target.value;state.visible=25;renderRows();});
    $('tool-filter').addEventListener('change',e=>{state.tool=e.target.value;state.visible=25;renderRows();});
    $('clear-filters').addEventListener('click',()=>{state.search='';state.filter='all';state.tool='all';state.range='all';state.sort='newest';state.visible=25;$('search-input').value='';$('tool-filter').value='all';$('range-filter').value='all';$('sort-filter').value='newest';syncFilter();renderRows();$('search-input').focus();});
    $('sort-filter').addEventListener('change',e=>{state.sort=e.target.value;state.visible=25;renderRows();});
    document.querySelectorAll('[data-filter]').forEach(btn=>btn.addEventListener('click',()=>{state.filter=btn.dataset.filter;state.visible=25;syncFilter();renderRows();}));
    $('more-btn').addEventListener('click',()=>{state.visible+=25;renderRows();});
    for(const id of ['drawer-close','drawer-done','drawer-backdrop'])$(id).addEventListener('click',closeDetails);
    $('copy-event').addEventListener('click',async()=>{const e=state.selected;if(!e)return;const data=[`Tool: ${e.tool||''}`,`Status: ${e.ok?'Success':'Failed'}`,`Time: ${e.time||''}`,`Duration: ${duration(e)}`,`Reason: ${e.reason||''}`,`Error: ${e.error||''}`,`SHA256: ${e.args_sha256||''}`].join('\n');try{await navigator.clipboard.writeText(data);toast('Details copied');}catch{toast('Copy unavailable in this browser');}});
    document.addEventListener('keydown',e=>{if(e.key==='Escape'){if(!$('detail-drawer').hidden)closeDetails();$('sidebar').classList.remove('open');}if(e.key==='/'&&$('detail-drawer').hidden&&!['INPUT','TEXTAREA'].includes(document.activeElement?.tagName)){e.preventDefault();$('search-input').focus();}});
    document.addEventListener('click',e=>{if(window.innerWidth<=760&&!e.target.closest('.sidebar,.mobile-menu'))$('sidebar').classList.remove('open');});
    setInterval(()=>{set('clock',timeFormat.format(new Date()));syncText();},1000);
    set('clock',timeFormat.format(new Date()));
    refresh();setInterval(()=>refresh(),3000);
  }
  setup();
})();
