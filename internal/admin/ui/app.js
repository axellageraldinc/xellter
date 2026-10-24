/* xellter admin panel. Plain JS on Tabler (vendor/tabler.min.css); talks to /admin/api/*. */
'use strict';

const $ = (s, e = document) => e.querySelector(s);
const S = { cfg: null, yaml: '', history: [], dirty: false, timer: null, rendering: false, saving: false };
const NAV = [['overview', 'Overview', 'layout-dashboard'], ['clients', 'Clients', 'users'], ['usage', 'Usage', 'chart-bar'], ['settings', 'Settings', 'settings'], ['config', 'Config file', 'file-code']];
const KIND = { chat: ['Text', 'message'], images: ['Image', 'photo'], videos: ['Video', 'video'], audio_speech: ['Text to speech', 'volume'], audio_transcription: ['Speech to text', 'microphone'], music: ['Music', 'music'] };

// ---------- DOM + API helpers ----------
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked' || k === 'selected' || k === 'disabled') el[k] = !!v;
    else if (k === 'readonly') el.readOnly = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
function icon(name, cls = '') {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('viewBox', '0 0 24 24'); s.setAttribute('class', 'icon ' + cls); s.setAttribute('fill', 'none'); s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '2'); s.setAttribute('stroke-linecap', 'round'); s.setAttribute('stroke-linejoin', 'round');
  s.innerHTML = ICONS[name] || ''; // static markup from icons.js
  return s;
}
const iconBtn = (title, ic, onclick, off) => h('button', { class: 'btn btn-sm btn-ghost-secondary px-1 flex-shrink-0', title, 'aria-label': title, onclick, disabled: off }, icon(ic));
const btn = (label, cls, onclick, ic) => h('button', { class: 'btn ' + cls, onclick }, ic && icon(ic, label ? 'me-1' : ''), label);

async function api(path, opt = {}, retried) {
  let r;
  try {
    r = await fetch('/admin/api/' + path, {
      method: opt.method || 'GET', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-Xellter': '1' },
      body: opt.body === undefined ? undefined : JSON.stringify(opt.body),
    });
  } catch (e) { // network hiccup or a restart in progress: try once more before giving up
    if (!retried && (opt.method || 'GET') === 'GET') { await new Promise(res => setTimeout(res, 700)); return api(path, opt, true); }
    throw new Error('Cannot reach xellter right now (the service may be restarting).');
  }
  if (r.status === 401 && path !== 'login') { showLogin(); throw new Error('login required'); }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}

function toast(msg, bad) {
  const t = $('#toast'); t.textContent = msg; t.classList.toggle('bad', !!bad); t.classList.add('show');
  clearTimeout(t._t); t._t = setTimeout(() => t.classList.remove('show'), bad ? 6000 : 2500);
}
const fail = e => toast(e.message || String(e), true);
const act = (p, okMsg) => p.then(() => { if (okMsg) toast(okMsg); S.cfg = null; return render(); }, fail);

// idr formats rupiah: whole rupiah for big amounts, and no trailing zeros
// on the small ones (Rp 0.132 reads better than Rp 0.1320).
const idr = n => {
  if (n == null) return '–';
  if (n === 0) return '0';
  if (n >= 100) return Math.round(n).toLocaleString('en-US');
  const s = (n >= 1 ? n.toFixed(2) : n.toFixed(4)).replace(/0+$/, '');
  return s.endsWith('.') ? s.slice(0, -1) : s;
};
const num = n => (n || 0).toLocaleString('en-US');
const human = s => s < 90 ? Math.round(s) + ' s' : s < 5400 ? Math.round(s / 60) + ' min' : s < 172800 ? Math.round(s / 3600) + ' h' : Math.round(s / 86400) + ' days';
const ago = t => human(Math.max(0, (Date.now() - new Date(t)) / 1000));
const left = t => { const s = (new Date(t) - Date.now()) / 1000; return s <= 0 ? '' : human(s); };
const zero = t => !t || String(t).startsWith('0001');

const badge = (text, color = 'secondary') => h('span', { class: `badge bg-${color}-lt me-1` }, text);
const dot = color => h('span', { class: `status-dot bg-${color} me-2` });
const progress = (used, total, neutral) => { // neutral = a share of a total, not a limit: never turns red
  const p = total > 0 ? Math.min(100, used / total * 100) : 0;
  return h('div', { class: 'progress progress-sm' }, h('div', { class: 'progress-bar ' + (neutral || p < 80 ? 'bg-primary' : p < 97 ? 'bg-warning' : 'bg-danger'), style: `width:${p}%` }));
};
const keyLabel = k => !k ? '' : k.includes('/') ? k : `${k} (before migration)`;
const card = (title, body, o = {}) => h('div', { class: 'card ' + (o.class || '') },
  (title || o.actions) && h('div', { class: 'card-header' }, h('h3', { class: 'card-title' }, title), o.actions && h('div', { class: 'card-actions' }, o.actions)),
  h('div', { class: 'card-body' + (o.flush ? ' p-0' : '') }, body));
const pl = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
const empty = text => h('div', { class: 'text-secondary py-2' }, text);
const flags = fl => (fl || []).filter(f => /^(free|plan|promo)/.test(f)).map(f => badge(f.startsWith('promo') ? f.split(' until')[0] : f, f === 'free' ? 'green' : f === 'plan' ? 'blue' : 'orange'));
// stat is a top-line number card. The cards sit in a .stat-row grid with
// equal rows, so a card with a longer label never makes its row taller than
// the others.
const stat = (ic, color, value, label) => h('div', { class: 'card card-sm stat-card' },
  h('div', { class: 'card-body d-flex' }, h('div', { class: 'row g-2 w-100 align-items-center' },
    h('div', { class: 'col-auto' }, h('span', { class: `bg-${color} text-white avatar` }, icon(ic))),
    h('div', { class: 'col' },
      h('div', { class: 'stat-value' }, value),
      h('div', { class: 'stat-label text-secondary' }, label)))));

// statRow lays the stat cards out on a grid: two per row on a phone, four on
// a desktop, and every row has the same height.
const statRow = cards => h('div', { class: 'stat-row' }, cards.map(c => stat(c[0], c[1], c[2], c[3])));

function setHead({ crumbs, title, sub, actions }) {
  $('#head').replaceChildren(h('div', { class: 'head-row' },
    h('div', { class: 'head-main' }, crumbs && h('div', { class: 'crumbs mb-1' }, crumbs), h('h2', { class: 'page-title mb-1' }, title), sub && h('div', { class: 'text-secondary' }, sub)),
    actions && h('div', { class: 'head-actions' }, actions)));
}

// ---------- forms: they edit S.cfg in place and mark it dirty ----------
function markDirty() { S.dirty = true; $('#savebar').hidden = false; }
function field(obj, key, o = {}) {
  const type = o.type || 'text', label = o.label || key, set = v => { obj[key] = v; markDirty(); };
  let input;
  if (type === 'switch') {
    input = h('input', { class: 'form-check-input', type: 'checkbox', onchange: e => set(e.target.checked) }); input.checked = !!obj[key];
    return h('label', { class: 'form-check form-switch mb-2' }, input, h('span', { class: 'form-check-label' }, label), o.help && h('div', { class: 'form-hint' }, o.help));
  }
  if (type === 'optswitch') {
    input = h('select', { class: 'form-select', onchange: e => { obj[key] = e.target.value === '' ? null : e.target.value === 'true'; markDirty(); } }, h('option', { value: '' }, 'Same as global'), h('option', { value: 'true' }, 'On'), h('option', { value: 'false' }, 'Off'));
    input.value = obj[key] == null ? '' : String(obj[key]);
  } else if (type === 'select') {
    input = h('select', { class: 'form-select', onchange: e => set(o.cast ? o.cast(e.target.value) : e.target.value) }, o.opts.map(x => h('option', { value: Array.isArray(x) ? x[0] : x }, Array.isArray(x) ? x[1] : x)));
    const cur = String(obj[key] ?? (Array.isArray(o.opts[0]) ? o.opts[0][0] : o.opts[0]));
    if (![...input.options].some(x => x.value === cur)) input.append(h('option', { value: cur }, o.custom ? o.custom(cur) : cur));
    input.value = cur;
  } else if (type === 'num') {
    input = h('input', { class: 'form-control', type: 'number', step: o.step || 'any', onchange: e => set(e.target.value === '' ? 0 : Number(e.target.value)), value: obj[key] ?? 0 });
  } else if (type === 'list') {
    input = h('input', { class: 'form-control', placeholder: o.ph || 'comma separated', onchange: e => set(e.target.value.split(',').map(s => s.trim()).filter(Boolean)), value: (obj[key] || []).join(', ') });
  } else if (type === 'lines') {
    input = h('textarea', { class: 'form-control', rows: 4, onchange: e => set(e.target.value.split('\n').map(s => s.trim()).filter(Boolean)) }); input.value = (obj[key] || []).join('\n');
  } else {
    input = h('input', { class: 'form-control', onchange: e => set(e.target.value), value: obj[key] ?? '', placeholder: o.ph, readonly: o.readonly });
  }
  return h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, label), input, o.help && h('div', { class: 'form-hint' }, o.help));
}
const cols = (...kids) => h('div', { class: 'row g-3' }, kids.flat(Infinity).filter(Boolean).map(k => h('div', { class: 'col-md-6' }, k)));
const advanced = (...kids) => h('details', { class: 'adv mt-2' }, h('summary', {}, 'Advanced'), h('div', { class: 'pt-2' }, kids));

async function loadCfg() {
  const j = await api('config');
  S.cfg = j.config; S.yaml = j.yaml; S.history = j.history || []; S.dirty = false; $('#savebar').hidden = true;
  S.cfg.clients ||= []; S.cfg.benchmarks ||= {};
  S.cfg.clients.forEach(c => { c.keys ||= []; c.presets ||= {}; });
}
async function ensureCfg() { if (!S.cfg || !S.dirty) await loadCfg(); }
async function saveCfg() {
  try { await api('config', { method: 'PUT', body: { config: S.cfg } }); await loadCfg(); toast('Saved and applied'); render(); } catch (e) { fail(e); }
}

function dialog(...kids) {
  $('#dlgbody').replaceChildren(...kids.flat(Infinity).filter(Boolean));
  const d = $('#dlg'); if (!d.open) d.showModal();
}
const closeDialog = () => $('#dlg').close();
const dialogButtons = (...b) => h('div', { class: 'd-flex gap-2 justify-content-end mt-3' }, h('button', { class: 'btn btn-ghost-secondary', onclick: closeDialog }, 'Close'), b);
const winOrder = { five_hour: 0, '5h': 0, week: 1, month: 2 };
const winName = { five_hour: '5-hour window', '5h': '5-hour window', week: 'This week', month: 'This month' };

// ---------- routing ----------
function route() {
  const [path, q] = location.hash.replace(/^#\/?/, '').split('?');
  const [tab, name] = path.split('/');
  return { tab: tab || 'overview', name: name ? decodeURIComponent(name) : null, q: new URLSearchParams(q || '') };
}
function go(path) {
  if (S.dirty && !confirm('Discard unsaved changes?')) return;
  S.dirty = false; $('#savebar').hidden = true; S.cfg = null;
  if (location.hash === '#/' + path) render(); else location.hash = '#/' + path;
}

const views = {};

// ---------- Overview ----------
views.overview = async function () {
  const [o, hoursRaw] = await Promise.all([api('overview'), api('usage?group=hour&hours=24')]);
  const hours = hoursRaw || [];
  const t = hours.reduce((a, r) => ({ req: a.req + r.requests, err: a.err + r.errors, cost: a.cost + r.cost_idr, rtk: a.rtk + (r.rtk_saved || 0) }), { req: 0, err: 0, cost: 0, rtk: 0 });
  const lvl = { info: ['info-circle', 'azure'], warn: ['alert-triangle', 'orange'], bad: ['alert-triangle', 'red'] };
  const color = o.status === 'bad' ? 'danger' : o.status === 'warn' ? 'warning' : 'success';
  setHead({ title: 'Overview', sub: `Up ${human(o.uptime_s)} · ${o.catalog?.models || 0} models known to kenari`, actions: btn('Refresh catalog', 'btn-outline-secondary btn-sm', () => act(api('catalog/refresh', { method: 'POST' }), 'Catalog refreshed'), 'refresh') });
  const view = h('div', {},
    h('div', { class: `alert alert-${color} mb-3` }, h('div', { class: 'd-flex align-items-center' }, dot(o.status === 'bad' ? 'red' : o.status === 'warn' ? 'orange' : 'green'),
      h('strong', {}, o.status === 'ok' ? 'Everything is running normally' : o.status === 'bad' ? 'Something needs your attention' : 'Running, with warnings')),
      (o.alerts || []).map(a => h('div', { class: 'alert-row' }, icon(lvl[a.level][0], 'text-' + lvl[a.level][1]), h('div', { class: 'me-auto' }, a.text), a.client && h('a', { class: 'clickable text-nowrap', onclick: () => go('clients/' + a.client) }, 'Open')))),
    statRow([
      ['bolt', 'blue', num(t.req), 'requests in 24 h'], ['alert-triangle', t.err ? 'orange' : 'green', t.req ? (t.err / t.req * 100).toFixed(1) + '%' : '0%', 'failed in 24 h'],
      ['coin', 'azure', 'Rp ' + idr(t.cost), 'at list price, 24 h'], ['wallet', 'green', num(Math.round(t.rtk / 1000)) + ' k', 'saved by RTK, 24 h']]),
    h('div', { class: 'row row-cards' },
      h('div', { class: 'col-lg-5' }, card('Kenari plan & balance', planCard(o), { class: 'h-100' })),
      h('div', { class: 'col-lg-7' }, h('div', { class: 'mb-3' }, card('Requests in the last 24 hours', chart(hours, 24))),
        card('By client', (o.clients || []).length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table table-hover' }, h('tbody', {}, o.clients.map(c => h('tr', { class: 'clickable', onclick: () => go('clients/' + c.name) },
          h('td', {}, h('strong', {}, c.name)), h('td', { class: 'text-secondary' }, `${pl(c.keys.length, 'key')} · ${pl(c.presets.length, 'preset')}`), h('td', { class: 'text-end' }, `${num(c.requests_24h)} requests`),
          h('td', { class: 'text-end text-secondary' }, 'Rp ' + idr(c.cost_24h))))))) : empty('No clients yet.'), { flush: (o.clients || []).length > 0 }))));
  return { node: view, refresh: 10000 };
};

function planCard(o) {
  const ac = (o.accounts || []).find(a => a.plan) || (o.accounts || [])[0];
  const key = o.account_key || {};
  const wins = !ac ? [] : ac.windows ? Object.entries(ac.windows).map(([n, w]) => ({ n, used: w.used_rp, budget: w.budget_rp, reset: w.resets_at })) :
    ['5h', 'week', 'month'].map((n, i) => ({ n, used: ac.estimated_spend[i], budget: ac.plan_budget[i] })).filter(w => w.budget > 0);
  wins.sort((a, b) => (winOrder[a.n] ?? 9) - (winOrder[b.n] ?? 9));
  return h('div', {},
    !ac ? empty('No kenari key yet. Add one to a client.') : [
      h('div', { class: 'd-flex align-items-center flex-wrap mb-3' }, h('strong', { class: 'me-2 fs-3' }, ac.plan ? ac.plan : 'No plan'), ac.plan && badge(ac.plan_source === 'kenari' ? 'live from kenari' : 'estimated', ac.plan_source === 'kenari' ? 'green' : 'orange')),
      wins.map(w => h('div', { class: 'mb-3' }, h('div', { class: 'd-flex justify-content-between small mb-1' }, h('span', {}, winName[w.n] || w.n), h('span', { class: 'text-secondary' }, `Rp ${idr(w.used)} of ${idr(w.budget)}`)), progress(w.used, w.budget),
        w.reset && !zero(w.reset) && h('div', { class: 'text-secondary small mt-1' }, 'Resets in ' + (left(w.reset) || 'a moment')))),
      !zero(ac.balance_at) && h('div', { class: 'd-flex justify-content-between border-top pt-2 mb-2' }, h('span', { class: 'text-secondary' }, 'Balance'), h('strong', {}, 'Rp ' + idr(ac.balance_idr))),
      h('div', { class: 'd-flex justify-content-between' }, h('span', { class: 'text-secondary' }, 'Requests this minute'), h('span', {}, `${ac.rpm_used} / ${ac.rpm_limit || '∞'}`))],
    h('div', { class: 'mt-3 pt-3 border-top' }, key.configured
      ? h('div', { class: 'd-flex align-items-center gap-2 flex-wrap' }, icon('shield-check', 'text-green'), h('span', { class: 'me-auto small' }, 'Account key set: plan and balance are read from kenari.'),
        btn('Replace', 'btn-sm btn-outline-secondary', () => setAccountKey()), btn('Remove', 'btn-sm btn-ghost-danger', () => { if (confirm('Remove the account key? Plan numbers become estimates.')) act(api('account-key', { method: 'POST', body: { key: '' } }), 'Account key removed'); }))
      : h('div', {}, h('div', { class: 'small text-secondary mb-2' }, 'Optional: add a full-access key of your kenari account so xellter can read the real plan, usage and balance. It is never used to serve requests.'), btn('Add account key', 'btn-sm btn-outline-primary', () => setAccountKey(), 'key'))));
}
function setAccountKey() {
  const f = { key: '' };
  dialog(h('h3', {}, 'Kenari account key'), h('p', { class: 'text-secondary' }, 'A key without model or spend limits. It only reads plan, usage and balance.'),
    h('div', { class: 'mb-3' }, h('input', { class: 'form-control', type: 'password', placeholder: 'kn-…', autocomplete: 'off', oninput: e => f.key = e.target.value })),
    dialogButtons(btn('Save', 'btn-primary', async () => { try { await api('account-key', { method: 'POST', body: { key: f.key } }); closeDialog(); toast('Account key saved'); S.cfg = null; render(); } catch (e) { fail(e); } })));
}

// ---------- charts ----------
// One bar per time bucket, split into succeeded and failed requests.
// The axis is completed with empty buckets, so a quiet hour reads as zero
// instead of vanishing; the y axis prints the scale and every bar explains
// itself in a tooltip.
const CHARTS = new Set();
const pad2 = n => String(n).padStart(2, '0');

function niceMax(v) {
  if (v <= 1) return 1;
  const mag = 10 ** Math.floor(Math.log10(v)), n = v / mag;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * mag;
}

// bucketKey builds the key the store uses: "YYYY-MM-DD", "YYYY-MM-DD HH:00"
// or "YYYY-MM-DD HH:MM" (all UTC, the store groups in UTC too).
function bucketKey(d, unit) {
  const day = `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;
  if (unit === 'day') return day;
  if (unit === 'minute') return `${day} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`;
  return `${day} ${pad2(d.getUTCHours())}:00`;
}

// chartBuckets groups the rows into buckets and fills the gaps, so the axis
// covers the whole period even when nothing happened in a bucket. The window
// matches the server: only whole buckets count, so the first bucket lies
// fully inside the period.
function chartBuckets(series, unit, hours) {
  const map = new Map();
  for (const r of series || []) {
    const key = r.key;
    const o = map.get(key) || { key, req: 0, err: 0, tin: 0, tout: 0, cost: 0 };
    o.req += r.requests; o.err += r.errors; o.tin += r.in_tok; o.tout += r.out_tok; o.cost += r.cost_idr;
    map.set(key, o);
  }
  const now = new Date(), out = [];
  const add = key => out.push(map.get(key) || { key, req: 0, err: 0, tin: 0, tout: 0, cost: 0 });
  const stepMs = unit === 'minute' ? 60000 : unit === 'hour' ? 3600000 : 86400000;
  const count = unit === 'day' ? Math.ceil(hours / 24) : hours * (unit === 'minute' ? 60 : 1);
  // the first bucket must lie fully inside the window: round the start up
  const start = Math.ceil((now.getTime() - hours * 3600000) / stepMs) * stepMs;
  const end = unit === 'day'
    ? Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
    : Math.floor(now.getTime() / stepMs) * stepMs;
  for (let i = count - 1; i >= 0; i--) add(bucketKey(new Date(end - i * stepMs), unit));
  // buckets before the window are dropped: the server does the same
  return out.filter(p => p.key >= bucketKey(new Date(start), unit));
}

// bucketLabel turns a bucket key into something readable.
function bucketLabel(key, unit, wide) {
  if (unit === 'day') return key.slice(8, 10) + '/' + key.slice(5, 7);
  const hm = key.slice(11);
  if (unit === 'minute') return hm;
  return wide ? key.slice(8, 10) + '/' + key.slice(5, 7) + ' ' + hm : hm;
}

// chartUnit picks the bucket size from the window.
const chartUnit = hours => (hours <= 6 ? 'minute' : hours <= 72 ? 'hour' : 'day');

function chart(series, hours, o = {}) {
  const unit = o.unit || chartUnit(hours);
  const pts = chartBuckets(series, unit, hours);
  const total = pts.reduce((a, p) => ({ req: a.req + p.req, err: a.err + p.err, cost: a.cost + p.cost }), { req: 0, err: 0, cost: 0 });
  if (!total.req) return empty('No traffic in this period.');
  const unitName = { minute: 'minute', hour: 'hour', day: 'day' }[unit];
  const busiest = pts.reduce((a, p) => p.req > a.req ? p : a, pts[0]);
  const errPct = total.req ? (total.err / total.req * 100) : 0;
  const avg = total.req / pts.length;
  const avgTxt = avg >= 10 ? Math.round(avg) : avg.toFixed(1);

  const wrap = h('div', { class: 'chart-wrap' });
  const canvas = h('div', { class: 'chart-canvas' });
  const tip = h('div', { class: 'chart-tip', hidden: true });
  wrap.append(canvas, tip);
  const ctl = { wrap, canvas, tip, pts, unit, hours };
  CHARTS.add(ctl);
  requestAnimationFrame(() => drawChart(ctl));

  return h('div', {},
    h('div', { class: 'chart-summary mb-2' },
      h('span', {}, `${num(total.req)} requests`),
      h('span', { class: 'mx-1' }, '·'),
      h('span', { class: errPct > 0 ? 'text-danger' : '' }, `${num(total.err)} failed (${errPct.toFixed(1)}%)`),
      h('span', { class: 'mx-1' }, '·'),
      h('span', {}, `busiest ${bucketLabel(busiest.key, unit, hours > 24)} with ${num(busiest.req)}`),
      h('span', { class: 'mx-1' }, '·'),
      h('span', {}, `average ${avgTxt} per ${unitName}`)),
    h('div', { class: 'chart-legend mb-2' },
      h('span', {}, h('span', { class: 'sw', style: 'background: var(--tblr-primary)' }), 'succeeded'),
      h('span', {}, h('span', { class: 'sw', style: 'background: var(--tblr-danger)' }), 'failed'),
      h('span', { class: 'ms-auto' }, `one bar per ${unitName} (UTC)`)),
    wrap);
}

function drawChart(ctl) {
  const pts = ctl.pts, unit = ctl.unit, n = pts.length;
  const w = Math.max(240, Math.round(ctl.wrap.clientWidth || 640));
  const padL = 38, padR = 6, padT = 8, padB = 20;
  const plotH = Math.round(Math.min(200, Math.max(130, w * 0.32)));
  const W = w, H = plotH + padT + padB, plotW = W - padL - padR;
  const max = niceMax(Math.max(1, ...pts.map(p => p.req)));
  const bw = plotW / n, gap = bw > 6 ? 1 : 0;
  const NS = 'http://www.w3.org/2000/svg', svg = document.createElementNS(NS, 'svg');
  const mk = (t, attrs) => { const e = document.createElementNS(NS, t); for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v); return e; };
  svg.setAttribute('width', W); svg.setAttribute('height', H); svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', `Requests per ${unit}: ${pts.reduce((a, p) => a + p.req, 0)} requests, ${pts.reduce((a, p) => a + p.err, 0)} failed, in ${n} ${unit} buckets`);

  [0, 0.5, 1].forEach(f => { // grid lines with the value they stand for
    const y = padT + plotH - f * plotH;
    svg.append(mk('line', { x1: padL, x2: W - padR, y1: y, y2: y, class: 'chart-grid' }));
    const t = mk('text', { x: padL - 6, y, class: 'chart-axis', 'text-anchor': 'end', 'dominant-baseline': 'middle' });
    t.textContent = num(max * f);
    svg.append(t);
  });

  pts.forEach((p, i) => {
    const x = padL + i * bw, g = mk('g', {});
    const barX = x + gap, barW = Math.max(1, bw - gap * 2);
    const ok = Math.max(0, p.req - p.err);
    const yOk = padT + plotH - ok / max * plotH;
    if (ok > 0) g.append(mk('rect', { x: barX, y: yOk, width: barW, height: Math.max(1, ok / max * plotH), rx: 2, class: 'chart-bar' }));
    if (p.err > 0) { // the failed part sits on top of the succeeded one
      const hErr = Math.max(1, p.err / max * plotH);
      g.append(mk('rect', { x: barX, y: yOk - hErr, width: barW, height: hErr, rx: 2, class: 'chart-bar err' }));
    }
    const hit = mk('rect', { x, y: padT, width: bw, height: plotH, fill: 'transparent' });
    const show = () => {
      ctl.tip.replaceChildren(
        h('div', { class: 'fw-bold' }, bucketLabel(p.key, unit, ctl.hours > 24)),
        h('div', {}, `${num(p.req)} requests`),
        h('div', { class: p.err ? 'text-danger' : 'text-secondary' }, `${num(p.err)} failed${p.req ? ' (' + (p.err / p.req * 100).toFixed(0) + '%)' : ''}`),
        h('div', { class: 'text-secondary' }, `${num(p.tin)} in / ${num(p.tout)} out tokens`),
        h('div', { class: 'text-secondary' }, 'Rp ' + idr(p.cost)));
      ctl.tip.hidden = false;
      // keep the tooltip inside the card: it is centred on the bar
      const w = ctl.wrap.clientWidth, tw = ctl.tip.offsetWidth;
      const cx = padL + i * bw + bw / 2;
      ctl.tip.style.left = Math.max(tw / 2 + 2, Math.min(w - tw / 2 - 2, cx)) + 'px';
    };
    hit.addEventListener('mouseenter', show);
    hit.addEventListener('click', show);
    hit.addEventListener('mouseleave', () => { ctl.tip.hidden = true; });
    g.append(hit);
    svg.append(g);
  });

  // Time axis: the labels are spread evenly over the buckets and as many as
  // fit are kept. Every label is measured in the DOM, so a crowded axis
  // thins out instead of overlapping.
  const made = []; // every time label, so the ones that do not fit can go
  const mkLabel = i => {
    // the first and last label are pinned to the edge so they cannot hang out
    const first = i === 0, last = i === n - 1;
    const t = mk('text', { x: first ? padL : last ? W - padR : padL + i * bw + bw / 2, y: H - 5, class: 'chart-axis', 'text-anchor': first ? 'start' : last ? 'end' : 'middle' });
    t.textContent = bucketLabel(pts[i].key, unit, ctl.hours > 24);
    svg.append(t);
    made.push(t);
    return { i, el: t };
  };
  const probe = [0, Math.floor(n / 2), n - 1].map(mkLabel);
  ctl.canvas.replaceChildren(svg); // the labels must be in the DOM to be measured

  const boxOf = t => { const b = t.getBBox(); return { l: b.x, r: b.x + b.width }; };
  const labelGap = 12; // room between two labels
  const labelW = Math.max(...probe.map(p => boxOf(p.el).r - boxOf(p.el).l));
  probe.forEach(p => p.el.remove());
  // how many labels fit in the plot
  const room = plotW + labelGap;
  const maxLabels = Math.max(2, Math.min(n, Math.floor(room / (labelW + labelGap))));
  // candidates at a regular bucket step, so the times on the axis are evenly
  // spaced; the last bucket is always a candidate
  const step = Math.max(1, Math.round((n - 1) / Math.max(1, maxLabels - 1)));
  const cand = [];
  for (let i = 0; i < n; i += step) cand.push(i);
  if (cand[cand.length - 1] !== n - 1) cand.push(n - 1);
  const labs = cand.map(mkLabel);
  const boxes = labs.map(l => boxOf(l.el));
  const lastBox = boxes[boxes.length - 1];
  // keep a label only when it clears the last one and the one before it
  const kept = [];
  for (let i = 0; i < labs.length - 1; i++) {
    const b = boxes[i];
    if (!(lastBox.l > b.r + labelGap || lastBox.r < b.l - labelGap)) continue;
    if (kept.length && b.l <= boxes[kept[kept.length - 1]].r + labelGap) continue;
    kept.push(i);
  }
  kept.push(labs.length - 1);
  const keepSet = new Set(kept.map(i => labs[i].el));
  made.forEach(t => { if (!keepSet.has(t)) t.remove(); });
}

// Charts are redrawn on resize so the bars keep their real pixel width.
window.addEventListener('resize', () => { for (const c of CHARTS) drawChart(c); });

// ---------- Clients ----------
views.clients = async function () {
  const o = await api('overview');
  setHead({ title: 'Clients', sub: 'The apps that call xellter. Each one has its own kenari keys and its own presets.', actions: btn('Add client', 'btn-primary', addClient, 'plus') });
  const rows = o.clients || [];
  return h('div', {}, card(null, rows.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table table-hover' },
    h('thead', {}, h('tr', {}, ['Client', 'Kenari keys', 'Presets', 'Last 24 h', ''].map(x => h('th', {}, x)))),
    h('tbody', {}, rows.map(c => h('tr', { class: 'clickable', onclick: () => go('clients/' + c.name) },
      h('td', {}, dot(!c.enabled ? 'secondary' : c.keys.length ? 'green' : 'orange'), h('strong', {}, c.name), ' ', !c.enabled && badge('disabled')),
      h('td', {}, c.keys.length ? c.keys.map(k => h('span', { class: 'me-2 nowrap' }, k.label)) : h('span', { class: 'text-orange' }, 'none yet')),
      h('td', {}, c.presets.length ? c.presets.map(p => h('span', { class: 'me-3 nowrap', title: p.name }, icon(KIND[p.modality]?.[1] || 'message', 'me-1'), p.name)) : h('span', { class: 'text-secondary' }, 'appear once a key is added')),
      h('td', { class: 'text-secondary nowrap' }, `${num(c.requests_24h)} requests · Rp ${idr(c.cost_24h)}`),
      h('td', { class: 'text-end' }, icon('chevron-right', 'text-secondary'))))))) : empty('No clients yet. Add the first one.'), { flush: rows.length > 0 }));
};

function addClient() {
  const f = { name: '' };
  dialog(h('h3', {}, 'Add a client'), h('p', { class: 'text-secondary' }, 'A client is one app, for example "babu" or "aixell". You get an xellter key for it next; then give it one or more kenari keys.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'e.g. babu', oninput: e => f.name = e.target.value })),
    dialogButtons(btn('Create', 'btn-primary', async () => { try { const r = await api('clients', { method: 'POST', body: { action: 'create', name: f.name } }); S.cfg = null; showKey(f.name.trim().replace(/\s+/g, '-'), r.key, true); } catch (e) { fail(e); } })));
}

function showKey(name, key, thenOpen) {
  dialog(h('h3', {}, `xellter key for ${name}`), h('p', { class: 'text-secondary' }, `Copy it now; it is not stored and cannot be shown again. The app uses it as its API key (bearer token or x-api-key) with base URL ${location.origin}/v1`),
    h('div', { class: 'keybox mono' }, key), h('div', { class: 'd-flex gap-2 justify-content-end mt-3' }, btn('Copy', 'btn-outline-secondary', () => navigator.clipboard?.writeText(key).then(() => toast('Copied'), () => toast('Select the key and copy it', true)), 'copy'),
      btn(thenOpen ? 'Continue' : 'Done', 'btn-primary', () => { closeDialog(); thenOpen ? go('clients/' + name) : render(); })));
}

// ---------- One client ----------
const presetSort = (p, n) => { const i = Object.keys(KIND).indexOf(p.modality); return (i < 0 ? 9 : i) + (n === ({ chat: 'text', images: 'image', videos: 'video', audio_speech: 'tts', audio_transcription: 'stt', music: 'music' })[p.modality] ? '0' : '1' + n); };

views.client = async function (name) {
  await ensureCfg();
  const cl = S.cfg.clients.find(c => c.name === name);
  if (!cl) { setHead({ title: 'Client not found' }); return empty('This client does not exist.'); }
  const [o, rk] = await Promise.all([api('overview'), api('ranking?client=' + encodeURIComponent(name))]);
  const row = (o.clients || []).find(c => c.name === name) || { keys: [], presets: [] };
  setHead({ crumbs: [h('a', { class: 'clickable', onclick: () => go('clients') }, 'Clients'), ' / ', name], title: [name, ' ', !cl.enabled && badge('disabled', 'red')],
    sub: `${pl(row.keys.length, 'kenari key')} · ${pl(Object.keys(cl.presets).length, 'preset')} · xellter key ${cl.key_hint || ''}`,
    actions: [btn('Usage', 'btn-outline-secondary', () => go('usage?client=' + encodeURIComponent(name)), 'chart-bar'), btn('New xellter key', 'btn-outline-secondary', () => { if (confirm(`Create a new key for ${name}? The old key stops working at once.`)) rotateKey(name); }, 'refresh'),
      btn('', 'btn-outline-danger', () => { if (confirm(`Delete client ${name} and its kenari keys from xellter?`)) act(api('clients', { method: 'POST', body: { action: 'delete', name } }).then(() => { location.hash = '#/clients'; }), 'Client deleted'); }, 'trash')] });

  const keyRows = row.keys.map(k => {
    const u = cl.keys.find(x => x.name === k.label) || {};
    const paused = (!zero(k.cooldown_until) && left(k.cooldown_until)) || (!zero(k.breaker_until) && left(k.breaker_until));
    const st = !k.enabled ? ['secondary', 'Off'] : !k.has_secret ? ['red', 'No key value'] : paused ? ['orange', 'Paused ' + paused] : ['green', 'Ready'];
    return h('tr', {}, h('td', {}, h('strong', {}, k.label), k.last_error && h('div', { class: 'text-danger small' }, k.last_error)),
      h('td', {}, dot(st[0]), st[1]), h('td', {}, k.learned_allow?.length ? h('span', { title: k.learned_allow.join(', ') }, `${k.learned_allow.length} model${k.learned_allow.length === 1 ? '' : 's'}`) : 'All models'),
      h('td', { class: 'text-end nowrap' }, iconBtn('Settings', 'adjustments-horizontal', () => keySettings(u)), iconBtn('Replace key', 'key', () => replaceKey(name, k.label)),
        iconBtn('Remove key', 'trash', () => { if (confirm(`Remove key "${k.label}" from ${name}? It stays valid at kenari.`)) act(api('keys', { method: 'POST', body: { client: name, action: 'remove', name: k.label } }), 'Key removed'); })));
  });
  const presets = Object.keys(cl.presets).sort((a, b) => presetSort(cl.presets[a], a).localeCompare(presetSort(cl.presets[b], b)));
  return h('div', {},
    h('div', { class: 'mb-3' }, card('Kenari API keys', row.keys.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table' }, h('thead', {}, h('tr', {}, ['Key', 'State', 'Can use', ''].map(x => h('th', {}, x)))), h('tbody', {}, keyRows)))
      : h('div', { class: 'p-3' }, empty('No kenari key yet. Add one and xellter creates this client\'s presets from the models it can use.')),
      { flush: true, actions: btn('Add key', 'btn-primary btn-sm', () => addKey(name), 'plus') })),
    h('div', { class: 'd-flex align-items-center mb-1' }, h('h3', { class: 'mb-0 me-auto' }, 'Presets'), btn('Add preset', 'btn-outline-primary btn-sm', () => addPreset(cl), 'plus')),
    h('p', { class: 'text-secondary' }, 'Created automatically from the models this client\'s keys can call, duplicates merged. The app uses a preset name as the model, for example "text"; for every request xellter picks the best model behind it right now.'),
    presets.length ? h('div', { class: 'row row-cards mb-3' }, presets.map(n => h('div', { class: 'col-lg-6' }, presetCard(name, n, cl.presets[n], rk[n])))) : empty('Presets appear here once a key has been added and checked.'),
    card('Client settings', h('div', {}, cols(field(cl, 'enabled', { type: 'switch', label: 'Allowed to call xellter' }), field(cl, 'daily_budget_idr', { type: 'num', label: 'Daily spending limit (Rp, 0 = none)', help: 'counted at list price, free and plan traffic included' }), field(cl, 'note', { label: 'Note' })),
      advanced(cols(field(cl, 'rtk', { type: 'optswitch', label: 'Token saver (RTK)' }), field(cl, 'passthrough', { type: 'list', label: 'Real models the app may also call directly', ph: 'e.g. deepseek-v4-1-flash', help: 'normally empty: apps should call presets' }))))));
};

async function rotateKey(name) {
  try { const r = await api('clients', { method: 'POST', body: { action: 'rotate', name } }); S.cfg = null; showKey(name, r.key, false); } catch (e) { fail(e); }
}
function addKey(client) {
  const f = { name: '', key: '' };
  dialog(h('h3', {}, `Add a kenari key to ${client}`), h('p', { class: 'text-secondary' }, 'Paste a key from the kenari dashboard. It is stored on this server only, never in the config file or the repo. xellter checks which models the key may use and creates presets from them.'),
    h('div', { class: 'row g-3' }, h('div', { class: 'col-md-4' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'main', oninput: e => f.name = e.target.value })),
      h('div', { class: 'col-md-8' }, h('label', { class: 'form-label' }, 'API key'), h('input', { class: 'form-control', type: 'password', placeholder: 'kn-…', autocomplete: 'off', oninput: e => f.key = e.target.value }))),
    dialogButtons(btn('Add key', 'btn-primary', async () => { try { await api('keys', { method: 'POST', body: { client, action: 'add', name: f.name || 'main', key: f.key } }); closeDialog(); toast('Key added; checking which models it can use…'); S.cfg = null; render(); setTimeout(() => { S.cfg = null; render(); }, 4000); } catch (e) { fail(e); } })));
}
function replaceKey(client, name) {
  const f = { key: '' };
  dialog(h('h3', {}, `Replace key "${name}"`), h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'New API key'), h('input', { class: 'form-control', type: 'password', autocomplete: 'off', oninput: e => f.key = e.target.value })),
    dialogButtons(btn('Replace', 'btn-primary', async () => { try { await api('keys', { method: 'POST', body: { client, action: 'replace', name, key: f.key } }); closeDialog(); toast('Key replaced'); render(); } catch (e) { fail(e); } })));
}
function keySettings(u) {
  dialog(h('h3', {}, `Key "${u.name}"`), h('p', { class: 'text-secondary' }, 'Most keys need nothing here. xellter detects the account, plan and limits by itself.'),
    cols(field(u, 'enabled', { type: 'switch', label: 'Use this key for requests' }), field(u, 'account', { label: 'Kenari account', ph: 'main', help: 'keys of one account share its limits; change only with several accounts' }),
      field(u, 'tier', { type: 'select', label: 'Account tier', opts: [['auto', 'Detect'], ['free', 'Free'], ['topup', 'Topped up'], ['plan', 'Has a plan']] }), field(u, 'plan', { label: 'Plan name', help: 'only needed without an account key, when the tier is "Has a plan"' }),
      field(u, 'rpm', { type: 'num', label: 'Requests per minute (0 = kenari default)' }), field(u, 'daily_free', { type: 'num', label: 'Free requests per day (0 = default, -1 = unlimited)' }),
      field(u, 'allow', { type: 'list', label: 'Only use these models', ph: 'normally detected automatically' }), field(u, 'note', { label: 'Note' })),
    dialogButtons(btn('Save', 'btn-primary', async () => { S.saving = true; closeDialog(); await saveCfg(); S.saving = false; })));
  $('#dlg').addEventListener('close', () => { if (S.dirty && !S.saving) { S.cfg = null; S.dirty = false; $('#savebar').hidden = true; render(); } }, { once: true });
}

// ---------- Presets ----------
const OBJECTIVES = {
  cheapest: { label: 'Cheapest', w: { cost: 60, benchmark: 10, jev: 10, reliability: 15, context: 5 }, q: 0.35 },
  balanced: { label: 'Balanced', w: { cost: 25, benchmark: 35, jev: 20, reliability: 15, context: 5 }, q: 0.50 },
  quality: { label: 'Best quality', w: { cost: 2, benchmark: 55, jev: 25, reliability: 15, context: 3 }, q: 0.64 },
};
// Image, video and audio presets have no JEV judge and no context size to weigh: three objectives on cost, quality and reliability.
const MEDIA_OBJECTIVES = {
  cheapest: { label: 'Cheapest', w: { cost: 70, benchmark: 10, jev: 0, reliability: 20, context: 0 }, q: 0 },
  balanced: { label: 'Balanced', w: { cost: 40, benchmark: 40, jev: 0, reliability: 20, context: 0 }, q: 0 },
  quality: { label: 'Best quality', w: { cost: 10, benchmark: 70, jev: 0, reliability: 20, context: 0 }, q: 0 },
};
const objectivesFor = p => p.modality === 'chat' ? OBJECTIVES : MEDIA_OBJECTIVES;
const objectiveOf = p => Object.entries(objectivesFor(p)).find(([, o]) => Object.entries(o.w).every(([k, v]) => (p.weights || {})[k] === v))?.[0] || 'custom';
const PRESET_NAME = { chat: 'text', images: 'image', videos: 'video', audio_speech: 'tts', audio_transcription: 'stt', music: 'music' };
const MODALITIES = { chat: 'Text', images: 'Image', videos: 'Video', audio_speech: 'Text to speech', audio_transcription: 'Speech to text', music: 'Music' };

function presetCard(client, name, p, r) {
  p.require ||= {}; p.weights ||= {}; p.cost_profile ||= {};
  const isChat = p.modality === 'chat', obj = objectiveOf(p), [kind, ic] = KIND[p.modality] || ['', 'message'];
  const body = h('div', {},
    p.description && h('div', { class: 'text-secondary mb-3' }, p.description),
    h('div', { class: 'mb-3' }, h('div', { class: 'subheader' }, 'Serving now'),
      r?.candidates?.length ? r.candidates.slice(0, 3).map((c, i) => h('div', { class: 'model-line' }, h('span', { class: 'mono' + (i ? ' text-secondary' : ' fw-bold') }, c.model), flags(c.flags))) : h('span', { class: 'text-danger' }, 'Nothing available right now'),
      r?.trial?.length ? h('div', { class: 'text-secondary small mt-1' }, `${r.trial.length} new model${r.trial.length === 1 ? '' : 's'} on trial`) : null),
    h('div', { class: 'mb-3' }, h('div', { class: 'subheader' }, 'Optimise for'),
      h('div', { class: 'btn-group w-100' }, Object.entries(objectivesFor(p)).map(([k, o]) => h('button', { class: 'btn btn-sm ' + (obj === k ? 'btn-primary' : 'btn-outline-primary'), onclick: () => { p.weights = { ...o.w }; p.min_quality = o.q; markDirty(); render(); } }, o.label)), obj === 'custom' && h('button', { class: 'btn btn-sm btn-primary', disabled: true }, 'Custom')),
      h('div', { class: 'form-hint' }, obj === 'custom' ? 'Tuned by hand under Advanced. Pick an option to reset it.' : isChat ? 'Cheapest: lowest price among models that are good enough. Balanced: best quality for the price. Best quality: the strongest model, price matters little.' : 'Quality comes from the Arena leaderboard when the model is listed there, otherwise it is estimated from price.')),
    modelPicker(p, r),
    h('div', { class: 'mb-3' }, h('div', { class: 'subheader' }, 'When several models are equally good'),
      h('select', { class: 'form-select form-select-sm', onchange: e => { p.strategy = e.target.value; markDirty(); } }, h('option', { value: 'priority', selected: p.strategy === 'priority' }, 'Always use the best one'), h('option', { value: 'round_robin', selected: p.strategy === 'round_robin' }, 'Take turns between them'))),
    isChat && h('div', { class: 'mb-2' }, h('div', { class: 'subheader' }, 'The model must support'),
      field(p.require, 'tool_call', { type: 'switch', label: 'Tool calling (agents, function calls)' }), field(p.require, 'vision', { type: 'switch', label: 'Image input' }),
      field(p.require, 'min_context', { type: 'select', label: 'Context size', cast: Number, custom: v => `${Math.round(v / 1000)}k tokens (custom)`, opts: [[0, 'Any'], [32000, 'at least 32k tokens'], [64000, 'at least 64k tokens'], [128000, 'at least 128k tokens'], [256000, 'at least 256k tokens']] })),
    advanced(cols(
      field(p, 'description'), field(p, 'mode', { type: 'select', label: 'How it picks', opts: [['auto', 'Automatically (ranking)'], ['combo', 'Fixed list, in order']] }),
      field(p, 'models', { type: 'list', label: 'Fixed list of models', help: 'only used for "Fixed list"' }), field(p, 'allow', { type: 'list', label: 'Only consider models matching', ph: 'e.g. deepseek-*, glm-*' }), field(p, 'deny', { type: 'list', label: 'Never use models matching' }),
      field(p, 'min_quality', { type: 'num', step: '0.01', label: 'Minimum quality (0-1)' }), field(p, 'max_candidates', { type: 'num', label: 'How many models to keep' }), field(p, 'rr_band', { type: 'num', step: '0.01', label: '"Equally good" means within' }),
      ['cost', 'benchmark', 'jev', 'reliability', 'context'].map(k => field(p.weights, k, { type: 'num', label: `Weight: ${k}` })),
      [['input_tokens', 'Typical input tokens'], ['output_tokens', 'Typical output tokens'], ['cached_ratio', 'Share of input served from cache'], ['images', 'Images per request'], ['seconds', 'Seconds of audio/video'], ['chars', 'Characters of speech']].map(([k, l]) => field(p.cost_profile, k, { type: 'num', label: l }))),
  ));
  return card(h('span', { class: 'd-flex align-items-center gap-2' }, h('span', { class: 'kind-icon' }, icon(ic)), h('span', {}, name, h('div', { class: 'text-secondary small fw-normal' }, `${kind} · ${r ? r.callable : 0} model${r?.callable === 1 ? '' : 's'} available`))), body,
    { actions: h('div', { class: 'd-flex gap-1' }, btn('Why this order?', 'btn-sm btn-ghost-secondary', () => showRanking(client, name)), btn('Delete', 'btn-sm btn-ghost-danger', () => { const cl = S.cfg.clients.find(c => c.name === client); if (confirm(`Delete preset "${name}" of ${client}? Apps calling it will get an error until you add it again with "Add preset".`)) { delete cl.presets[name]; markDirty(); render(); } }, 'trash')), class: 'h-100' });
}

// modelPicker lists every model the client's keys can call for this preset and lets the owner untick the ones it must never use.
function modelPicker(p, r) {
  const all = (r && r.models) || [];
  if (!all.length) return null;
  p.deny ||= [];
  const off = id => p.deny.includes(id);
  const body = h('div', { class: 'pt-2' }, all.map(id => h('label', { class: 'form-check mb-1' },
    h('input', { class: 'form-check-input', type: 'checkbox', checked: !off(id), onchange: e => { p.deny = e.target.checked ? p.deny.filter(x => x !== id) : [...p.deny, id]; markDirty(); render(); } }),
    h('span', { class: 'form-check-label mono' }, id, !(r.candidates || []).concat(r.trial || []).some(c => c.model === id) && !off(id) && h('span', { class: 'text-secondary' }, ' · not used right now')))),
    h('div', { class: 'form-hint' }, 'Unticked models are never used by this preset. New models that appear later are on by default (and start on trial).'));
  const n = all.filter(id => !off(id)).length;
  return h('details', { class: 'adv mb-2' }, h('summary', {}, `Models it may use: ${n} of ${all.length}`), body);
}

function addPreset(cl) {
  // Presets are predefined (text, image, ...) and a client has each at most once, so only the missing ones are offered.
  const missing = Object.keys(MODALITIES).filter(k => !cl.presets[PRESET_NAME[k]]);
  if (!missing.length) return toast('This client already has every preset', true);
  const f = { kind: missing[0] };
  dialog(h('h3', {}, 'Add a preset'), h('p', { class: 'text-secondary' }, 'Presets are predefined, one of each kind. Pick one that was deleted or not created yet; it starts from the defaults.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Preset'), h('select', { class: 'form-select', onchange: e => f.kind = e.target.value },
      missing.map(k => h('option', { value: k }, `${PRESET_NAME[k]} (${MODALITIES[k].toLowerCase()})`)))),
    dialogButtons(btn('Add', 'btn-primary', () => {
      cl.presets[PRESET_NAME[f.kind]] = newDefault(f.kind);
      markDirty(); closeDialog(); render();
    })));
}
function newDefault(kind) {
  const media = { cost: 70, benchmark: 10, jev: 0, reliability: 20, context: 0 };
  if (kind === 'chat') return { description: '', modality: 'chat', mode: 'auto', require: { tool_call: true, min_context: 32000 }, min_quality: 0.50, max_candidates: 4, strategy: 'round_robin', rr_band: 0.04, weights: { ...OBJECTIVES.balanced.w }, cost_profile: { input_tokens: 4000, output_tokens: 600, cached_ratio: 0.7 } };
  return { description: '', modality: kind, mode: 'auto', require: {}, min_quality: 0, max_candidates: 3, strategy: 'priority', rr_band: 0.03, weights: media, cost_profile: { images: 1, seconds: kind === 'audio_transcription' ? 60 : 5, chars: 1000, variant: kind === 'videos' ? '720p' : '' } };
}

async function showRanking(client, name) {
  try {
    const r = await api(`ranking?client=${encodeURIComponent(client)}&preset=${encodeURIComponent(name)}`);
    const row = (c, i) => h('tr', {}, h('td', {}, i + 1), h('td', { class: 'mono' }, c.model), h('td', {}, c.score.toFixed(2)), h('td', { class: 'nowrap' }, c.cost_idr ? 'Rp ' + idr(c.cost_idr) : 'free / plan'),
      h('td', {}, (c.flags || []).map(f => badge(f, /free|plan|promo|proven/.test(f) ? 'green' : 'orange'))), h('td', { class: 'text-secondary small' }, Object.entries(c.components || {}).map(([k, v]) => `${k} ${v.toFixed(2)}`).join(' · ')));
    const tbl = list => h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm' }, h('thead', {}, h('tr', {}, ['#', 'Model', 'Score', 'Cost per typical request', 'Why', 'Scores'].map(x => h('th', {}, x)))), h('tbody', {}, list.map(row))));
    dialog(h('h3', {}, `${client} / ${name}: how the order is decided`), h('p', { class: 'text-secondary' }, 'Higher score wins. Models close to the top score take turns when the preset is set to do so.'),
      r.candidates?.length ? tbl(r.candidates) : h('p', { class: 'text-danger' }, 'No model qualifies right now.'),
      r.trial?.length ? [h('h4', {}, 'New models on trial'), h('p', { class: 'text-secondary' }, 'They get a small share of traffic until they have proven themselves.'), tbl(r.trial)] : null,
      r.rejected?.length ? [h('h4', {}, `Left out (${r.rejected.length})`), h('table', { class: 'table table-sm' }, h('tbody', {}, r.rejected.map(x => h('tr', {}, h('td', { class: 'mono' }, x.model), h('td', { class: 'text-secondary' }, x.why)))))] : null,
      r.unavailable?.length ? h('p', { class: 'text-secondary small' }, `${r.unavailable.length} more models exist but this client's keys may not call them.`) : null, dialogButtons());
  } catch (e) { fail(e); }
}

// ---------- Usage ----------
views.usage = async function (_, q) {
  const client = q.get('client') || '', dim = q.get('by') || 'preset', hours = +(q.get('h') || 24), filter = q.get('f') || 'all';
  const cq = client ? '&client=' + encodeURIComponent(client) : '';
  const unit = chartUnit(hours); // minute buckets for a short window, hours, then days
  const [o, rows0, series0, reqs0] = await Promise.all([api('overview'), api(`usage?group=${dim}&hours=${hours}${cq}`), api(`usage?group=${unit}&hours=${hours}${cq}`), api(`requests?limit=200${cq}`)]);
  const rows = rows0 || [], series = series0 || [], reqs = reqs0 || [];
  const t = series.reduce((a, r) => ({ req: a.req + r.requests, err: a.err + r.errors, cost: a.cost + r.cost_idr, tin: a.tin + r.in_tok, tout: a.tout + r.out_tok, lat: a.lat + r.avg_latency_ms * r.requests }), { req: 0, err: 0, cost: 0, tin: 0, tout: 0, lat: 0 });
  const to = patch => { const n = new URLSearchParams(q); for (const [k, v] of Object.entries(patch)) v === '' ? n.delete(k) : n.set(k, v); return 'usage?' + n; };
  const seg = (opts, cur, mk) => h('div', { class: 'btn-group' }, opts.map(([v, l]) => h('button', { class: 'btn btn-sm ' + (String(cur) === String(v) ? 'btn-primary' : 'btn-outline-primary'), onclick: () => go(mk(v)) }, l)));
  const max = Math.max(1, ...rows.map(r => r.requests));
  const shown = reqs.filter(r => filter === 'all' || !r.ok);
  setHead({ title: 'Usage', sub: client ? `Only ${client}` : 'All clients', actions: [
    h('select', { class: 'form-select form-select-sm', style: 'width:auto', onchange: e => go(to({ client: e.target.value })) }, h('option', { value: '' }, 'All clients'), (o.clients || []).map(c => h('option', { value: c.name, selected: c.name === client }, c.name))),
    seg([[1, '1 h'], [24, '24 h'], [168, '7 d'], [720, '30 d']], hours, v => to({ h: v }))] });
  const DIMS = [['preset', 'Preset'], ['model', 'Model'], ['client', 'Client'], ['upstream', 'Kenari key']];
  return h('div', {},
    statRow([
      ['bolt', 'blue', num(t.req), `requests · ${num(t.tin)} in / ${num(t.tout)} out tokens`], ['alert-triangle', t.err ? 'orange' : 'green', t.req ? (t.err / t.req * 100).toFixed(1) + '%' : '0%', `failed (${num(t.err)} of ${num(t.req)})`],
      ['clock', 'azure', t.req ? Math.round(t.lat / t.req) + ' ms' : '–', 'average time per request'], ['coin', 'green', 'Rp ' + idr(t.cost), 'at list price, free and plan included']]),
    h('div', { class: 'mb-3' }, card('Requests over time', chart(series, hours, { unit }))),
    h('div', { class: 'mb-3' }, card('Breakdown', h('div', {}, h('div', { class: 'mb-3' }, seg(DIMS, dim, v => to({ by: v }))),
      rows.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter' }, h('thead', {}, h('tr', {}, [DIMS.find(d => d[0] === dim)[1], 'Requests', '', 'Failed', 'Tokens in / out', 'Avg time', 'Rp at list price'].map(x => h('th', {}, x)))),
        h('tbody', {}, rows.filter(r => r.key).map(r => h('tr', {}, h('td', { class: 'mono' }, dim === 'upstream' ? keyLabel(r.key) : r.key), h('td', {}, num(r.requests)), h('td', { style: 'min-width:90px' }, progress(r.requests, max, true)), h('td', { class: r.errors ? 'text-danger' : 'text-secondary' }, r.errors), h('td', {}, `${num(r.in_tok)} / ${num(r.out_tok)}`), h('td', {}, Math.round(r.avg_latency_ms) + ' ms'), h('td', {}, idr(r.cost_idr))))))) : empty('No traffic in this period.')))),
    card('Recent requests', h('div', {}, h('div', { class: 'mb-3' }, seg([['all', 'All'], ['err', 'Errors only']], filter, v => to({ f: v }))),
      shown.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm table-vcenter' }, h('thead', {}, h('tr', {}, ['When', 'Client', 'Asked for', 'Served by', 'Key', 'Result', 'Time', 'Tokens in / out', 'Notes'].map(x => h('th', {}, x)))),
        h('tbody', {}, shown.slice(0, 100).map(r => h('tr', {}, h('td', { class: 'nowrap text-secondary' }, ago(r.ts) + ' ago'), h('td', {}, r.client), h('td', { class: 'mono' }, r.requested || r.preset), h('td', { class: 'mono' }, r.model), h('td', {}, keyLabel(r.upstream)),
          h('td', {}, badge(r.ok ? 'ok' : (r.status || 'error'), r.ok ? 'green' : 'red')), h('td', { class: 'nowrap' }, r.latency_ms + ' ms'), h('td', { class: 'nowrap' }, `${r.in_tok} / ${r.out_tok}`),
          h('td', {}, r.attempt > 1 && badge('retry ' + r.attempt, 'orange'), r.canary && badge('trial', 'azure'), r.rtk_saved > 0 && badge('saved ' + r.rtk_saved, 'green'), r.err && h('span', { class: 'text-danger small' }, r.err))))))) : empty('Nothing to show.'))));
};

// savedOrderDiffers is true while the edited list differs from the chain in effect.
function savedOrderDiffers(jev, j) {
  const live = (j.chain || []).filter(x => x.configured).map(x => x.id);
  const now = jev.judges || [];
  return live.length !== now.length || live.some((id, i) => id !== now[i]);
}

// judgeOrder edits jev.judges: the judges are tried from top to bottom, so the
// owner orders them instead of picking one.
function judgeOrder(jev, j) {
  jev.judges ||= [];
  const meta = id => (j.judges || []).find(x => x.id === id);
  const label = id => { const m = meta(id); return !m ? id : m.free ? `${id} (free)` : `${id} (Rp ${idr(m.in_idr)} per 1M input tokens)`; };
  const move = (i, by) => { const a = jev.judges, k = i + by; if (k < 0 || k >= a.length) return; [a[i], a[k]] = [a[k], a[i]]; markDirty(); render(); };
  const known = new Set(jev.judges);
  const addable = (j.judges || []).filter(x => !known.has(x.id));
  const picker = h('select', { class: 'form-select form-select-sm', style: 'max-width:22rem' },
    addable.length ? addable.map(x => h('option', { value: x.id }, label(x.id))) : h('option', { value: '' }, 'No other judge in the catalog'));
  return h('div', { class: 'mb-3' },
    h('label', { class: 'form-label' }, 'Judges, in order'),
    jev.judges.length ? h('ol', { class: 'list-unstyled mb-2' }, jev.judges.map((id, i) => h('li', { class: 'd-flex align-items-center gap-1 py-1' },
      h('span', { class: 'text-secondary mono me-1' }, i + 1 + '.'),
      h('span', { class: 'mono me-auto' + (meta(id) ? '' : ' text-warning') }, label(id), !meta(id) && h('span', { class: 'small' }, ' · not in the catalog right now')),
      i === 0 && badge('tried first', 'blue'),
      iconBtn('Move up', 'arrow-up', () => move(i, -1), i === 0),
      iconBtn('Move down', 'arrow-down', () => move(i, 1), i === jev.judges.length - 1),
      iconBtn('Remove', 'trash', () => { jev.judges.splice(i, 1); markDirty(); render(); })))) : empty('No judge chosen yet.'),
    h('div', { class: 'd-flex gap-2' }, picker, btn('Add', 'btn-outline-primary btn-sm', () => { if (picker.value) { jev.judges.push(picker.value); markDirty(); render(); } }, 'plus')),
    h('div', { class: 'form-hint' }, 'The judge at the top scores every answer. If kenari cannot serve it (down or too slow), the next one in this list takes over, and after that any other free System One judge. A judge that is not free costs money from the daily limit, so keep free ones first.'));
}

// ---------- Settings ----------
views.settings = async function () {
  await ensureCfg();
  const c = S.cfg, j = await api('jev'), R = c.rules, cat = await api('catalog'), bn = await api('bench');
  const bench = Object.entries(c.benchmarks);
  const L = { free_bonus: 'Preference for free models', plan_bonus: 'Preference for plan-covered models', plan_cost_factor: 'Cost of plan-covered models (0-1)', promo_bonus_max: 'Preference for discounted models',
    promo_min_remaining_hours: 'Ignore discounts ending within (h)', upcoming_lookahead_hours: 'Look ahead for price changes (h)', new_model_days: 'A model is "new" for (days)', canary_min_requests: 'Requests to trust a new model', canary_min_success: 'Success rate to trust it (0-1)', stats_window_hours: 'Health window (h)' };
  const RL = { max_attempts: 'Models to try per request', cooldown_base_s: 'First pause after an error (s)', cooldown_max_s: 'Longest pause (s)', breaker_failures: 'Errors in a row before a key is paused', breaker_open_s: 'How long a paused key rests (s)' };
  setHead({ title: 'Settings', sub: 'Sensible defaults are already set. Most of this you never need to touch.' });
  return h('div', { class: 'row row-cards' },
    h('div', { class: 'col-lg-6' }, card('Smart routing', h('div', {},
      field(R, 'canary_percent', { type: 'num', label: 'Try new models on this share of traffic (%)', help: 'new models are tried in small doses before they are trusted' }),
      field(R, 'sticky_minutes', { type: 'num', label: 'Keep one conversation on one model (minutes)', help: 'keeps the provider cache warm and answers consistent' }),
      field(R, 'avoid_sunset_days', { type: 'num', label: 'Stop using models that end within (days)' }),
      advanced(cols(['free_bonus', 'plan_bonus', 'plan_cost_factor', 'promo_bonus_max'].map(k => field(R, k, { type: 'num', step: '0.01', label: L[k] })), ['promo_min_remaining_hours', 'upcoming_lookahead_hours', 'new_model_days', 'canary_min_requests', 'canary_min_success', 'stats_window_hours'].map(k => field(R, k, { type: 'num', label: L[k] }))))), { class: 'h-100' })),
    h('div', { class: 'col-lg-6' }, card('Token saver (RTK)', h('div', {}, h('p', { class: 'text-secondary' }, 'Shrinks big tool output (git diff, grep, ls, logs) before it is sent and paid for. Your own messages are never touched.'),
      field(c.rtk, 'enabled', { type: 'switch', label: 'Token saver on' }),
      h('div', { class: 'row' }, [['git_diff', 'git diff'], ['grep', 'grep results'], ['ls', 'directory listings'], ['logs', 'logs'], ['truncate', 'any very long output']].map(([k, l]) => h('div', { class: 'col-6' }, field(c.rtk, k, { type: 'switch', label: l })))),
      advanced(cols(field(c.rtk, 'min_chars', { type: 'num', label: 'Only outputs longer than (characters)' }), field(c.rtk, 'max_chars', { type: 'num', label: 'Hard cap (characters)' }), field(c.rtk, 'diff_context', { type: 'num', label: 'Context lines kept around diff changes' })))), { class: 'h-100' })),
    h('div', { class: 'col-lg-6' }, card('Quality judge (JEV)', h('div', {}, h('p', { class: 'text-secondary' }, 'Candidate models answer a few test questions and kenari\'s System One model scores each answer. The score decides which models are good enough for a preset. Judging costs a little, so a daily limit applies.'),
      h('div', { class: 'mb-2' }, badge(j.enabled ? 'on' : 'off', j.enabled ? 'green' : 'secondary'), badge(`today Rp ${idr(j.spent_idr)} of Rp ${idr(j.budget_idr)}`)),
      field(c.jev, 'enabled', { type: 'switch', label: 'Judge models automatically' }),
      judgeOrder(c.jev, j),
      (j.chain || []).length > 1 && h('div', { class: 'small text-secondary mb-3' }, savedOrderDiffers(c.jev, j) ? 'Saved order (press Save to apply your changes): ' : 'In effect now: ', j.chain.map((x, i) => h('span', {}, (i ? ' → ' : ''), h('span', { class: 'mono' + (x.configured ? '' : ' text-secondary') }, x.id), x.configured ? '' : ' (automatic)'))),
      j.system_key ? h('div', { class: 'small text-secondary mb-3' }, icon('shield-check', 'me-1 text-green'), 'Uses the dashboard\'s own account key (the one that reads plan and balance), never a client\'s key. Judging calls show up as "_system" in the request list.')
        : h('div', { class: 'alert alert-warning py-2' }, 'Judging needs the account key. Add it on the ', h('a', { class: 'clickable', onclick: () => go('overview') }, 'Overview'), ' page, under Kenari plan & balance.'),
      field(c.jev, 'daily_budget_idr', { type: 'num', label: 'Daily limit (Rp)' }),
      j.last && j.last.at && !String(j.last.at).startsWith('0001') ? h('div', { class: 'small mb-2 ' + (j.last.error ? 'text-danger' : 'text-secondary') }, `Last round ${ago(j.last.at)} ago: judged ${j.last.judged} model${j.last.judged === 1 ? '' : 's'}${j.last.error ? ' · ' + j.last.error : ''}`) : h('div', { class: 'small text-secondary mb-2' }, 'No round yet. The first one starts about a minute after you switch it on and save.'),
      btn('Judge pending models now', 'btn-outline-primary btn-sm mb-3', () => api('jev/run', { method: 'POST', body: {} }).then(() => { toast('Started in the background'); setTimeout(render, 8000); }, fail)),
      Object.keys(j.scores || {}).length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm' }, h('thead', {}, h('tr', {}, ['Model', 'Score', 'Judged'].map(x => h('th', {}, x)))), h('tbody', {}, Object.entries(j.scores).sort((a, b) => b[1].Score - a[1].Score).map(([m, s]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', {}, s.Score.toFixed(2)), h('td', { class: 'text-secondary' }, ago(s.TS) + ' ago')))))) : h('div', { class: 'text-secondary small' }, 'No scores yet.'),
      advanced(cols(field(c.jev, 'pass_threshold', { type: 'num', step: '0.01', label: 'Minimum score to be used (0-1)' }), field(c.jev, 'every_hours', { type: 'num', label: 'Re-judge every (hours)' })), field(c.jev, 'prompts', { type: 'lines', label: 'Test questions (one per line)' }))), { class: 'h-100' })),
    h('div', { class: 'col-lg-6' }, card('Quality scores', h('div', {},
      h('p', { class: 'text-secondary' }, 'How good each model is. xellter downloads the public Arena leaderboard (human votes on text, image and video models) every day and matches it to your models. A score is the chance the model wins a vote against a typical model. Models the leaderboard does not list are estimated from price until you set a score or JEV judges them.'),
      h('div', { class: 'mb-2' }, bn.models ? [badge(`${bn.models} models scored`, 'green'), badge(`leaderboard of ${bn.latest || '?'}`), badge(`downloaded ${zero(bn.fetched) ? 'never' : ago(bn.fetched) + ' ago'}`)] : badge('no leaderboard downloaded yet', 'orange')),
      (bn.kinds || []).map(k => h('div', { class: 'd-flex justify-content-between small' }, h('span', {}, { chat: 'Text', images: 'Image', videos: 'Video' }[k.kind] || k.kind), h('span', { class: 'text-secondary' }, `${k.matched} of ${k.total} paid models matched`))),
      btn('Update now', 'btn-outline-primary btn-sm my-3', () => api('bench/refresh', { method: 'POST', body: {} }).then(() => { toast('Leaderboards updated'); render(); }, fail), 'refresh'),
      h('div', { class: 'subheader mt-2' }, 'Your own scores (override the leaderboard)'),
      h('datalist', { id: 'models' }, cat.map(m => h('option', { value: m.id }))),
      bench.length ? h('table', { class: 'table table-sm' }, h('tbody', {}, bench.map(([m, v]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', { style: 'width:110px' }, h('input', { class: 'form-control form-control-sm', type: 'number', min: 0, max: 100, value: v, onchange: e => { c.benchmarks[m] = Number(e.target.value); markDirty(); } })), h('td', { class: 'text-end' }, btn('Remove', 'btn-sm btn-ghost-danger', () => { delete c.benchmarks[m]; markDirty(); render(); })))))) : h('div', { class: 'text-secondary small mb-2' }, 'None set.'),
      btn('Add a score', 'btn-outline-primary btn-sm', () => { const m = prompt('Model id (e.g. deepseek-v4-1-flash):'); if (m) { c.benchmarks[m.trim()] = 70; markDirty(); render(); } }, 'plus'),
      advanced(h('div', { class: 'small text-secondary' }, 'Source: ' + bn.source + '. Not matched yet: ', (bn.kinds || []).flatMap(k => k.unmatched || []).slice(0, 40).join(', ') || 'none'))), { class: 'h-100' })),
    h('div', { class: 'col-12' }, card('Reliability and access', h('div', {}, advanced(cols(
      ['max_attempts', 'cooldown_base_s', 'cooldown_max_s', 'breaker_failures', 'breaker_open_s'].map(k => field(c.resilience, k, { type: 'num', label: RL[k] })),
      field(c.server, 'admin_hosts', { type: 'list', label: 'Addresses allowed to open this panel' }), field(c.server, 'session_hours', { type: 'num', label: 'Stay signed in for (hours)' }), field(c.server, 'log_retention_days', { type: 'num', label: 'Keep the request log (days)' }), field(c.kenari, 'sync_minutes', { type: 'num', label: 'Refresh prices every (minutes)' }))),
      btn('Sign out everywhere', 'btn-outline-secondary btn-sm mt-3', () => api('logout-all', { method: 'POST' }).then(() => location.reload(), fail), 'logout')))));
};

// ---------- Config file ----------
views.config = async function () {
  await loadCfg();
  const ta = h('textarea', { class: 'form-control mono', rows: 22, spellcheck: 'false' }); ta.value = S.yaml;
  setHead({ title: 'Config file', sub: 'The same settings as plain text. Saving checks everything first; the previous version is kept and can be restored.' });
  return h('div', {}, h('div', { class: 'mb-3' }, card(null, h('div', {}, ta, h('div', { class: 'd-flex gap-2 mt-3' }, btn('Check, save & apply', 'btn-primary', async () => { try { await api('config', { method: 'PUT', body: { yaml: ta.value } }); toast('Saved and applied'); render(); } catch (e) { fail(e); } }), btn('Reload', 'btn-ghost-secondary', () => render()))))),
    card('Earlier versions', S.history.length ? S.history.slice(0, 20).map(id => h('div', { class: 'd-flex justify-content-between align-items-center py-1' }, h('span', { class: 'mono' }, id), btn('Restore', 'btn-sm btn-outline-secondary', async () => { if (!confirm('Restore ' + id + '?')) return; try { await api('rollback', { method: 'POST', body: { id } }); toast('Restored'); render(); } catch (e) { fail(e); } }))) : empty('No earlier versions yet.')));
};

// ---------- shell ----------
async function render() {
  if (S.rendering) return;
  S.rendering = true;
  const r = route(), root = $('#view');
  const active = r.tab;
  [...$('#nav').children].forEach(li => li.firstChild.classList.toggle('active', li.firstChild.dataset.tab === active));
  $('#menu').classList.remove('show');
  clearInterval(S.timer);
  try {
    const out = r.tab === 'clients' && r.name ? await views.client(r.name) : await (views[r.tab] || views.overview)(r.name, r.q);
    root.replaceChildren(out && out.node ? out.node : out);
    if (out && out.refresh) S.timer = setInterval(() => { if (!document.hidden && !$('#dlg').open && !S.dirty) { S.polling = true; render().finally(() => { S.polling = false; }); } }, out.refresh);
  } catch (e) {
    if (e.message !== 'login required') {
      if (S.polling && root.childNodes.length) toast(e.message, true); else root.replaceChildren(h('div', { class: 'alert alert-danger' }, e.message));
    }
  } finally { S.rendering = false; }
}

function showLogin() { $('#app').hidden = true; $('#login').hidden = false; }
function showApp() { $('#login').hidden = true; $('#app').hidden = false; }

$('#nav').append(...NAV.map(([id, label, ic]) => h('li', { class: 'nav-item' }, h('a', { class: 'nav-link clickable', 'data-tab': id, onclick: () => go(id) }, icon(ic), h('span', { class: 'nav-link-title' }, label)))));
$('#theme').append(icon('sun-moon'), 'Light / dark');
$('#logout').append(icon('logout'), 'Sign out');
$('#burger').onclick = () => $('#menu').classList.toggle('show');
$('#save').onclick = async () => { S.saving = true; await saveCfg(); S.saving = false; };
$('#discard').onclick = async () => { await loadCfg(); render(); };
$('#logout').onclick = () => api('logout', { method: 'POST' }).then(showLogin, fail);
$('#theme').onclick = () => { const t = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark'; document.documentElement.setAttribute('data-bs-theme', t); try { localStorage.setItem('xl-theme', t); } catch (e) { /* storage may be blocked */ } };
$('#loginform').addEventListener('submit', async e => {
  e.preventDefault(); const f = new FormData(e.target); $('#loginerr').textContent = '';
  try { await api('login', { method: 'POST', body: { username: f.get('username'), password: f.get('password'), code: f.get('code') } }); e.target.reset(); showApp(); render(); }
  catch (err) { $('#loginerr').textContent = err.message; }
});
window.addEventListener('hashchange', () => { if (S.dirty) { S.dirty = false; $('#savebar').hidden = true; } S.cfg = null; render(); });
window.addEventListener('beforeunload', e => { if (S.dirty) { e.preventDefault(); e.returnValue = ''; } });
api('me').then(() => { showApp(); render(); }, () => showLogin());
