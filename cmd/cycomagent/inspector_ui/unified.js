(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const mc = $('mc-view');
  const runtime = $('runtime-view');
  const inspector = $('inspector-view');
  const crumb = $('page-crumb');
  const title = inspector?.querySelector('.page-heading h1');
  const intro = inspector?.querySelector('.page-heading p');
  const metricGrid = inspector?.querySelector('.metric-grid');
  const analytics = $('overview-panels');
  const system = $('system-panel');
  const activity = $('activity-section');
  const labels = {
    overview: ['Overview', 'Mission Control', 'A quick view of runtime status and connected devices.'],
    runtime: ['Runtime Details', 'Runtime Details', 'Jobs, targets, traces, workflows, sessions and registered capabilities.'],
    activity: ['Activity Log', 'Activity Log', 'Inspect tool executions, timings and audit details.'],
    failures: ['Failures', 'Failed Executions', 'Investigate unsuccessful calls and inspect their error details.'],
    system: ['System Health', 'System Health', 'Monitor the local audit stream, runtime status and self-healing signals.']
  };
  function activate(name, {history = false, scroll = true} = {}) {
    if (!labels[name]) name = 'overview';
    const isOverview = name === 'overview';
    const isRuntime = name === 'runtime';
    const isInspector = !isOverview && !isRuntime;
    if (mc) mc.hidden = !isOverview;
    if (runtime) runtime.hidden = !isRuntime;
    if (inspector) inspector.hidden = !isInspector;
    if (metricGrid) metricGrid.hidden = name !== 'activity';
    if (analytics) analytics.hidden = name !== 'activity';
    if (system) system.hidden = name !== 'system';
    if (activity) activity.hidden = !['activity', 'failures'].includes(name);
    if (crumb) crumb.textContent = labels[name][0];
    if (title) title.textContent = labels[name][1];
    if (intro) intro.textContent = labels[name][2];
    document.querySelectorAll('.nav-link[data-view]').forEach(button => {
      const active = button.dataset.view === name;
      button.classList.toggle('selected', active);
      if (active) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    });
    $('sidebar')?.classList.remove('open');
    if (history && location.hash !== `#${name}`) window.history.pushState({page:name}, '', `#${name}`);
    if (scroll) window.scrollTo({top:0, behavior:'instant'});
  }
  document.querySelectorAll('.nav-link[data-view]').forEach(button => {
    button.addEventListener('click', () => activate(button.dataset.view, {history:true}));
  });
  window.addEventListener('popstate', () => activate(location.hash.slice(1) || 'overview', {scroll:false}));
  window.addEventListener('hashchange', () => activate(location.hash.slice(1) || 'overview', {scroll:false}));
  activate(location.hash.slice(1) || 'overview', {scroll:false});
})();
