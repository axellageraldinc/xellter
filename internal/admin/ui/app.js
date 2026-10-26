/* xellter admin panel. Plain JS on Tabler (vendor/tabler.min.css); talks to /admin/api/*. */
'use strict';

const $ = (s, e = document) => e.querySelector(s);
const S = { cfg: null, yaml: '', history: [], purposes: [], dirty: false, timer: null, rendering: false, saving: false };
const NAV = [['overview', 'Overview', 'layout-dashboard'], ['clients', 'Clients', 'users'], ['usage', 'Usage', 'chart-bar'], ['config', 'Config file', 'file-code']];
const KIND = { chat: ['Text', 'message'], images: ['Image', 'photo'], videos: ['Video', 'video'], audio_speech: ['Text to speech', 'volume'], audio_transcription: ['Speech to text', 'microphone'], music: ['Music', 'music'], systemone: ['System One', 'bolt'] };

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
// iconBtn is an icon-only button: the icon sits in the middle of a square, so
// a row of them (rename, replace, delete) lines up and the icons do not wobble.
// o.cls styles it, o.off disables it, o.sm makes it the small size (use it
// inside tables; in a button row use the normal size, so every button there
// has the same height).
const iconBtn = (title, ic, onclick, o = {}) => h('button', { class: `btn icon-btn ${o.sm ? 'btn-sm' : ''} ${o.cls || 'btn-ghost-secondary'}`, title, 'aria-label': title, onclick, disabled: o.off }, icon(ic));
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
// field renders one setting. Only the two kinds the panel needs are left:
// a switch and a number. Everything else is a config-file edit (ADR 0012).
function field(obj, key, o = {}) {
  const type = o.type || 'num', label = o.label || key, set = v => { obj[key] = v; markDirty(); };
  let input;
  if (type === 'switch') {
    input = h('input', { class: 'form-check-input', type: 'checkbox', onchange: e => set(e.target.checked) }); input.checked = !!obj[key];
    return h('label', { class: 'form-check form-switch mb-2' }, input, h('span', { class: 'form-check-label' }, label), o.help && h('div', { class: 'form-hint' }, o.help));
  }
  input = h('input', { class: 'form-control', type: 'number', step: o.step || 'any', onchange: e => set(e.target.value === '' ? 0 : Number(e.target.value)), value: obj[key] ?? 0 });
  return h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, label), input, o.help && h('div', { class: 'form-hint' }, o.help));
}

async function loadCfg() {
  const j = await api('config');
  S.cfg = j.config; S.yaml = j.yaml; S.history = j.history || []; S.dirty = false; $('#savebar').hidden = true;
  S.cfg.clients ||= [];
  S.cfg.clients.forEach(c => { c.keys ||= []; c.presets ||= {}; });
}
async function ensureCfg() { if (!S.cfg || !S.dirty) await loadCfg(); }
async function saveCfg() {
  try { await api('config', { method: 'PUT', body: { config: S.cfg } }); await loadCfg(); toast('Saved and applied'); render(); } catch (e) { fail(e); }
}

// dialog shows a modal. The optional last argument {wide: true} makes it
// wider, for tables that need the room.
function dialog(...kids) {
  const last = kids[kids.length - 1];
  const opts = last && typeof last === 'object' && !last.nodeType && last.__opts ? (kids.pop().__opts) : {};
  $('#dlgbody').replaceChildren(...kids.flat(Infinity).filter(Boolean));
  const d = $('#dlg');
  d.classList.toggle('dlg-wide', !!opts.wide);
  if (!d.open) d.showModal();
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
      (o.alerts || []).map(a => h('div', { class: 'alert-row' }, icon(lvl[a.level][0], 'text-' + lvl[a.level][1]), h('div', { class: 'me-auto' }, a.text), a.client && h('a', { class: 'clickable text-nowrap', onclick: () => go('clients/' + encodeURIComponent(a.client)) }, 'Open')))),
    statRow([
      ['bolt', 'blue', num(t.req), 'requests in 24 h'], ['alert-triangle', t.err ? 'orange' : 'green', t.req ? (t.err / t.req * 100).toFixed(1) + '%' : '0%', 'failed in 24 h'],
      ['coin', 'azure', 'Rp ' + idr(t.cost), 'at list price, 24 h'], ['wallet', 'green', num(Math.round(t.rtk / 1000)) + ' k', 'saved by RTK, 24 h']]),
    h('div', { class: 'row row-cards' },
      h('div', { class: 'col-lg-5' }, card('Kenari plan & balance', planCard(o), { class: 'h-100' })),
      h('div', { class: 'col-lg-7' }, h('div', { class: 'mb-3' }, card('Requests in the last 24 hours', chart(hours, 24))),
        card('By client', (o.clients || []).length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table table-hover' }, h('tbody', {}, o.clients.map(c => h('tr', { class: 'clickable', onclick: () => go('clients/' + encodeURIComponent(c.name)) },
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
  const padL = 38, padT = 8, padB = 20;
  const plotH = Math.round(Math.min(200, Math.max(130, w * 0.32)));
  // the room a time label needs on each side, so the first and the last one
  // sit centred on their bucket instead of being pulled to the edge
  const labelPad = 16;
  const W = w, H = plotH + padT + padB, plotW = Math.max(60, W - padL - labelPad);
  const max = niceMax(Math.max(1, ...pts.map(p => p.req)));
  const bw = plotW / n, gap = bw > 6 ? 1 : 0;
  const NS = 'http://www.w3.org/2000/svg', svg = document.createElementNS(NS, 'svg');
  const mk = (t, attrs) => { const e = document.createElementNS(NS, t); for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v); return e; };
  svg.setAttribute('width', W); svg.setAttribute('height', H); svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', `Requests per ${unit}: ${pts.reduce((a, p) => a + p.req, 0)} requests, ${pts.reduce((a, p) => a + p.err, 0)} failed, in ${n} ${unit} buckets`);

  [0, 0.5, 1].forEach(f => { // grid lines with the value they stand for
    const y = padT + plotH - f * plotH;
    svg.append(mk('line', { x1: padL, x2: padL + plotW, y1: y, y2: y, class: 'chart-grid' }));
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

  // Time axis: the labels sit on a regular step of buckets, so the times on
  // the axis are evenly spaced. Every label is measured in the DOM and the
  // ones that would touch a label already on the axis are dropped, so a
  // crowded axis thins out instead of overlapping.
  const made = []; // every time label, so the ones that do not fit can go
  const mkLabel = i => {
    // centred on its bucket: the same anchor everywhere keeps the spacing
    // between two labels equal to the distance between their buckets
    const t = mk('text', { x: padL + i * bw + bw / 2, y: H - 5, class: 'chart-axis', 'text-anchor': 'middle' });
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
  // one regular step in buckets, so the labelled times stay evenly spaced
  const step = Math.max(1, Math.ceil((n - 1) / (maxLabels - 1)));
  const cand = [];
  for (let i = 0; i < n; i += step) cand.push(i);
  const labs = cand.map(mkLabel);
  const boxes = labs.map(l => boxOf(l.el));
  const kept = [];
  for (let i = 0; i < labs.length; i++) {
    const b = boxes[i];
    if (kept.length && b.l <= boxes[kept[kept.length - 1]].r + labelGap) continue;
    kept.push(i);
  }
  const keepSet = new Set(kept.map(i => labs[i].el));
  made.forEach(t => { if (!keepSet.has(t)) t.remove(); });
}

// Charts are redrawn on resize so the bars keep their real pixel width.
window.addEventListener('resize', () => { for (const c of CHARTS) drawChart(c); });

// ---------- Clients ----------
// The list is one row per client: name, purpose, enabled, how many kenari keys
// and presets it has, and what it did in the last 24 hours.
views.clients = async function () {
  const o = await api('overview');
  S.purposes = o.purposes || S.purposes;
  setHead({ title: 'Clients', sub: 'The apps that call xellter. Each one has its own kenari keys, one purpose and its own presets.', actions: btn('New client', 'btn-primary', addClient, 'plus') });
  const rows = o.clients || [];
  return h('div', {}, card(null, rows.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table table-hover' },
    h('thead', {}, h('tr', {}, ['Client', 'Purpose', 'Kenari keys', 'Presets', 'Last 24 h', ''].map(x => h('th', {}, x)))),
    h('tbody', {}, rows.map(c => h('tr', { class: 'clickable', onclick: () => go('clients/' + encodeURIComponent(c.name)) },
      h('td', {}, dot(!c.enabled ? 'secondary' : c.keys.some(k => k.state === 'ready') ? 'green' : 'orange'), h('strong', {}, c.name), ' ', !c.enabled && badge('disabled', 'red')),
      h('td', {}, badge(c.purpose_label || c.purpose, 'azure')),
      h('td', {}, c.keys.length ? c.keys.map(k => h('span', { class: 'me-2 nowrap', title: k.state }, k.name)) : h('span', { class: 'text-orange' }, 'none yet')),
      h('td', {}, c.presets.length ? c.presets.map(p => h('span', { class: 'me-3 nowrap', title: `${p.label} · ${p.optimization_label}` }, icon(KIND[p.category]?.[1] || 'message', 'me-1'), p.name)) : h('span', { class: 'text-secondary' }, 'none yet')),
      h('td', { class: 'text-secondary nowrap' }, `${num(c.requests_24h)} requests · Rp ${idr(c.cost_24h)}`),
      h('td', { class: 'text-end' }, icon('chevron-right', 'text-secondary'))))))) : empty('No clients yet. Add the first one.'), { flush: rows.length > 0 }));
};

function addClient() {
  const f = { name: '', purpose: 'general', key: '' };
  dialog(h('h3', {}, 'New client'), h('p', { class: 'text-secondary' }, 'A client is one app, for example "babu" or "aixell". You get an xellter key for it next.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'e.g. babu', oninput: e => f.name = e.target.value })),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Purpose'), h('select', { class: 'form-select', onchange: e => f.purpose = e.target.value },
      (S.purposes || []).map(p => h('option', { value: p.value, selected: p.value === f.purpose }, p.label))),
      h('div', { class: 'form-hint' }, 'How this client\'s text preset is scored. It reorders the pool, it never changes which models are in it.')),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Kenari key (optional)'), h('input', { class: 'form-control', type: 'password', placeholder: 'kn-…', autocomplete: 'off', oninput: e => f.key = e.target.value }),
      h('div', { class: 'form-hint' }, 'Stored on this server only. You can add more keys later on the client page.')),
    dialogButtons(btn('Create', 'btn-primary', async () => {
      try {
        const r = await api('clients', { method: 'POST', body: { action: 'create', name: f.name, purpose: f.purpose, key: f.key } });
        closeDialog();
        showKey(f.name.trim().replace(/\s+/g, '-'), r.key, true);
      } catch (e) { fail(e); }
    })));
}

function showKey(name, key, thenOpen) {
  dialog(h('h3', {}, `xellter key for ${name}`), h('p', { class: 'text-secondary' }, `Copy it now; it is not stored and cannot be shown again. The app uses it as its API key (bearer token or x-api-key) with base URL ${location.origin}/v1`),
    h('div', { class: 'keybox mono' }, key), h('div', { class: 'd-flex gap-2 justify-content-end mt-3' }, btn('Copy', 'btn-outline-secondary', () => navigator.clipboard?.writeText(key).then(() => toast('Copied'), () => toast('Select the key and copy it', true)), 'copy'),
      btn(thenOpen ? 'Continue' : 'Done', 'btn-primary', () => { closeDialog(); thenOpen ? go('clients/' + encodeURIComponent(name)) : render(); })));
}

// ---------- One client ----------
// The whole configuration of one client on one page: purpose, kenari keys, and
// one card per preset with its optimization, its model picker and what it
// serves right now.
views.client = async function (name) {
  const d = await api('client?name=' + encodeURIComponent(name));
  const cl = d.client;
  S.purposes = d.purposes;
  setHead({ crumbs: [h('a', { class: 'clickable', onclick: () => go('clients') }, 'Clients'), ' / ', cl.name],
    title: [cl.name, ' ', !cl.enabled && badge('disabled', 'red')],
    sub: `${pl(d.keys.length, 'kenari key')} · ${pl(d.presets.length, 'preset')} · xellter key ${cl.key_hint || 'none'}`,
    actions: [
      btn('Usage', 'btn-outline-secondary', () => go('usage?client=' + encodeURIComponent(cl.name)), 'chart-bar'),
      btn('Look up a session', 'btn-outline-secondary', () => sessionLookup(cl.name), 'clock'),
      btn('New xellter key', 'btn-outline-secondary', () => { if (confirm(`Create a new key for ${cl.name}? The old key stops working at once.`)) rotateKey(cl.name); }, 'refresh'),
      btn('Recalculate', 'btn-outline-secondary', () => act(api('recompute?client=' + encodeURIComponent(cl.name), { method: 'POST' }).then(() => { S.cfg = null; }), 'Rankings recalculated'), 'adjustments-horizontal'),
      iconBtn('Delete client', 'trash', () => { if (confirm(`Delete client ${cl.name} and its kenari keys from xellter?`)) act(api('clients', { method: 'POST', body: { action: 'delete', name: cl.name } }).then(() => { location.hash = '#/clients'; }), 'Client deleted'); }, { cls: 'btn-outline-danger' }),
    ] });

  const purpose = h('select', { class: 'form-select form-select-sm', style: 'width:auto', onchange: e => act(api('clients', { method: 'POST', body: { action: 'update', name: cl.name, purpose: e.target.value, enabled: cl.enabled, note: cl.note } }), 'Purpose saved') },
    d.purposes.map(p => h('option', { value: p.value, selected: p.value === cl.purpose }, p.label)));
  const enabled = h('input', { class: 'form-check-input', type: 'checkbox', onchange: e => act(api('clients', { method: 'POST', body: { action: 'update', name: cl.name, purpose: cl.purpose, enabled: e.target.checked, note: cl.note } }), e.target.checked ? 'Client enabled' : 'Client disabled') });
  enabled.checked = cl.enabled;

  const settings = card('Settings', h('div', { class: 'row g-3' },
    h('div', { class: 'col-md-4' }, h('label', { class: 'form-label' }, 'Purpose'), purpose),
    h('div', { class: 'col-md-4' }, h('label', { class: 'form-label d-block' }, 'Serving'), h('label', { class: 'form-check form-switch' }, enabled, h('span', { class: 'form-check-label' }, cl.enabled ? 'Enabled' : 'Disabled'))),
    h('div', { class: 'col-md-4' }, h('label', { class: 'form-label' }, 'Rankings'), h('div', { class: 'text-secondary small pt-2' }, zero(d.computed_at) ? 'not computed yet' : 'computed ' + ago(d.computed_at) + ' ago'),
      h('div', { class: 'form-hint' }, `Refreshed every ${d.routing.rank_refresh_minutes || '—'} min, after every catalog sync, and by Recalculate.`))));

  const keyRows = d.keys.map(k => {
    const st = k.state === 'ready' ? ['green', 'Ready'] : k.state === 'paused' ? ['orange', 'Paused ' + (left(k.cooldown_until) || left(k.breaker_until) || 'a moment')] : k.state === 'unchecked' ? ['azure', 'Not checked yet'] : ['red', 'No key value'];
    return h('tr', {},
      h('td', {}, h('strong', {}, k.name), k.last_error && h('div', { class: 'text-danger small' }, k.last_error)),
      h('td', { class: 'mono text-secondary' }, k.hint || '—'),
      h('td', {}, dot(st[0]), st[1]),
      h('td', {}, k.unrestricted ? 'All models' : k.allowed ? `${num(k.allowed)} models` : k.probed ? 'none' : '—',
        k.blocked > 0 && h('span', { class: 'text-orange small ms-2' }, `${k.blocked} blocked now`)),
      h('td', { class: 'text-end nowrap' },
        iconBtn('Rename key', 'pencil', () => renameKey(cl.name, k.name), { sm: true }),
        iconBtn('Replace key', 'key', () => replaceKey(cl.name, k.name), { sm: true }),
        iconBtn('Remove key', 'trash', () => { if (confirm(`Remove key "${k.name}" from ${cl.name}? It stays valid at kenari.`)) act(api('keys', { method: 'POST', body: { client: cl.name, action: 'remove', name: k.name } }), 'Key removed'); }, { sm: true, cls: 'btn-ghost-danger' })));
  });

  const keys = card('Kenari API keys', d.keys.length
    ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table' },
      h('thead', {}, h('tr', {}, ['Key', 'Hint', 'Probe', 'Can use', ''].map(x => h('th', {}, x)))), h('tbody', {}, keyRows)))
    : h('div', { class: 'p-3' }, empty('No kenari key yet. Add one; xellter asks kenari which models it may call.')),
    { flush: true, actions: btn('Add key', 'btn-primary btn-sm', () => addKey(cl.name), 'plus') });

  const have = new Set(d.presets.map(p => p.category));
  const missing = d.categories.filter(c => !have.has(c.category));
  return h('div', {},
    h('div', { class: 'row row-cards mb-3' },
      h('div', { class: 'col-lg-7' }, h('div', { class: 'h-100' }, keys)),
      h('div', { class: 'col-lg-5' }, h('div', { class: 'h-100' }, settings))),
    h('div', { class: 'd-flex align-items-center mb-1' }, h('h3', { class: 'mb-0 me-auto' }, 'Presets'),
      missing.length > 0 && btn('Add preset', 'btn-outline-primary btn-sm', () => addPreset(cl.name, missing), 'plus')),
    h('p', { class: 'text-secondary' }, 'A preset is the model name an app asks for, for example "text". Behind it is the pool you pick here, ordered by the optimization you choose.'),
    d.presets.length
      ? h('div', { class: 'row row-cards mb-3' }, d.presets.map(p => h('div', { class: 'col-xl-6' }, presetCard(cl, p))))
      : empty('No presets yet. Add one for each category this client uses.'));
};

async function rotateKey(name) {
  try { const r = await api('clients', { method: 'POST', body: { action: 'rotate', name } }); S.cfg = null; showKey(name, r.key, false); } catch (e) { fail(e); }
}
function addKey(client) {
  const f = { name: '', key: '' };
  dialog(h('h3', {}, `Add a kenari key to ${client}`), h('p', { class: 'text-secondary' }, 'Paste a key from the kenari dashboard. It is stored on this server only, never in the config file or the repo. xellter then asks kenari which models the key may use.'),
    h('div', { class: 'row g-3' }, h('div', { class: 'col-md-4' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'main', oninput: e => f.name = e.target.value })),
      h('div', { class: 'col-md-8' }, h('label', { class: 'form-label' }, 'API key'), h('input', { class: 'form-control', type: 'password', placeholder: 'kn-…', autocomplete: 'off', oninput: e => f.key = e.target.value }))),
    dialogButtons(btn('Add key', 'btn-primary', async () => { try { await api('keys', { method: 'POST', body: { client, action: 'add', name: f.name || 'main', key: f.key } }); closeDialog(); toast('Key added; asking kenari which models it can use…'); S.cfg = null; render(); setTimeout(render, 4000); } catch (e) { fail(e); } })));
}
function replaceKey(client, name) {
  const f = { key: '' };
  dialog(h('h3', {}, `Replace key "${name}"`), h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'New API key'), h('input', { class: 'form-control', type: 'password', autocomplete: 'off', oninput: e => f.key = e.target.value })),
    dialogButtons(btn('Replace', 'btn-primary', async () => { try { await api('keys', { method: 'POST', body: { client, action: 'replace', name, key: f.key } }); closeDialog(); toast('Key replaced'); render(); } catch (e) { fail(e); } })));
}
// renameKey edits the only thing a key has besides its value: its name.
function renameKey(client, name) {
  const f = { name };
  dialog(h('h3', {}, `Rename key "${name}"`), h('p', { class: 'text-secondary' }, 'The name is how you recognise the key here. xellter detects everything else (account, plan, limits) by itself.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', value: name, oninput: e => f.name = e.target.value })),
    dialogButtons(btn('Save', 'btn-primary', async () => {
      try { await api('keys', { method: 'POST', body: { client, action: 'rename', name, new_name: f.name } }); closeDialog(); toast('Key renamed'); render(); } catch (e) { fail(e); }
    })));
}

// sessionLookup answers "what is this conversation using right now?" without
// asking the client: it reads the same pin the router uses.
function sessionLookup(client) {
  const f = { id: '' };
  const out = h('div', { class: 'mt-3' });
  dialog(h('h3', {}, `Session of ${client}`), h('p', { class: 'text-secondary' }, 'Paste a session id (X-Xellter-Session, X-Session-Id, prompt_cache_key, or the id the app sends).'),
    h('div', { class: 'd-flex gap-2' }, h('input', { class: 'form-control mono', placeholder: 'session id', oninput: e => f.id = e.target.value }),
      btn('Look up', 'btn-primary', async () => {
        try {
          const s = await api(`session?client=${encodeURIComponent(client)}&id=${encodeURIComponent(f.id)}`);
          const u = s.session, pins = Object.entries(s.pins || {});
          out.replaceChildren(
            h('div', { class: 'mb-2' }, h('span', { class: 'text-secondary' }, 'Preset '), h('strong', {}, u.preset || '—'), ' ', badge(`${num(u.requests)} requests`, 'azure'), u.errors ? badge(`${num(u.errors)} errors`, 'red') : null),
            pins.length ? pins.map(([preset, p]) => h('div', { class: 'model-line' }, icon('key', 'text-secondary'), h('span', { class: 'mono' }, p.model), badge(`pinned to ${preset}`, 'blue'), h('span', { class: 'text-secondary small ms-auto' }, 'for ' + human(p.live_for_s)))) : empty('No live pin: the session has not been routed yet.'),
            h('div', { class: 'text-secondary small mt-2' }, `${num(u.in_tok)} in / ${num(u.out_tok)} out tokens · ${num(u.cached_tok)} cached · Rp ${idr(u.cost_idr)} · models ${(u.models || []).join(', ') || '—'}`));
        } catch (e) { out.replaceChildren(h('div', { class: 'text-danger' }, e.message)); }
      })),
    out, dialogButtons());
}

// ---------- Presets ----------
const OPTIMIZATIONS = [['cheapest', 'Cheapest'], ['balanced', 'Balanced'], ['best_quality', 'Best Quality']];
const OPT_HELP = {
  cheapest: 'Price only: input, output, cache and promos. Five free and five plan-covered models, so the two never compete.',
  balanced: 'Half price, half quality from public benchmarks.',
  best_quality: 'Benchmark of this category only. Models with no evidence are left out.',
};
const BILLING = { free: 'green', plan: 'blue', balance: 'secondary' };
const BILLING_TEXT = { free: 'free', plan: 'plan', balance: 'balance' };

// rp formats a catalog price per 1M tokens the way the owner reads it: Rp100k,
// Rp1.2M, Rp500. The catalog publishes whole rupiah per million tokens.
const rp = n => {
  if (n == null) return '—';
  if (n === 0) return 'Rp0';
  if (n >= 1e6) return 'Rp' + (n / 1e6).toFixed(n % 1e6 ? 1 : 0) + 'M';
  if (n >= 1e3) return 'Rp' + Math.round(n / 1e3) + 'k';
  return 'Rp' + idr(n);
};
// ctx is the context window in the compact form the picker uses: 1.0M, 512k, 0.
const ctx = n => !n ? '—' : n >= 1e6 ? (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M' : Math.round(n / 1e3) + 'k';
// unitLine is the per-unit price of an image, video, speech or music model,
// which has no token prices at all.
const unitLine = r => (r.lines || []).map(l => `${rp(l.idr)} / ${l.unit}${l.variant ? ' ' + l.variant : ''}`).join(' · ');
// priceLine is the exact line the owner asked for: In, Out, cache read, cache
// write, in IDR. A price the catalog does not publish is a dash, not a zero; a
// free model publishes Rp0 for the token prices it does have. A model sold per
// image, second, character or song has no token price at all, so its unit price
// takes the line instead.
const priceLine = r => {
  const unit = unitLine(r);
  if (!r.free && !r.in_idr && !r.out_idr && unit) return unit;
  const p = v => v > 0 ? rp(v) : r.free ? 'Rp0' : '—';
  const cw = r.has_cache_write ? rp(r.cache_write_idr) : '—';
  return `In ${p(r.in_idr)}, Out ${p(r.out_idr)}, CR ${p(r.cache_read_idr)}, CW ${cw}`;
};

function presetCard(cl, p) {
  const [kind, ic] = KIND[p.category] || ['', 'message'];
  const r = p.ranking || { candidates: [], rejected: [], unranked: [] };
  // The card edits a draft and posts it in one call, so a half-picked family
  // never reaches the router.
  const draft = { optimization: p.optimization, models: [...(p.models || [])] };
  const same = (a, b) => a.length === b.length && [...a].sort().join('\n') === [...b].sort().join('\n');
  const dirty = () => draft.optimization !== p.optimization || !same(draft.models, p.models || []);

  const badgeDirty = h('span', { class: 'badge bg-orange-lt', hidden: true }, 'unsaved');
  const saveBtn = btn('Save', 'btn-primary btn-sm', () => act(api('presets', { method: 'POST', body: { client: cl.name, action: 'save', preset: p.name, category: p.category, optimization: draft.optimization, models: draft.models } }), 'Preset saved'), 'check');
  saveBtn.disabled = true;
  const sync = () => { const on = dirty(); saveBtn.disabled = !on; badgeDirty.hidden = !on; };

  const opt = h('select', { class: 'form-select form-select-sm', onchange: e => { draft.optimization = e.target.value; optHelp.textContent = OPT_HELP[draft.optimization] || ''; sync(); } },
    OPTIMIZATIONS.map(([v, l]) => h('option', { value: v, selected: v === draft.optimization }, l)));
  const optHelp = h('div', { class: 'form-hint' }, OPT_HELP[draft.optimization] || '');

  // ---- Serving now: the ranked list, then what was left out and why ----
  const candidates = r.candidates || [];
  const serving = h('div', { class: 'mb-3' },
    h('div', { class: 'subheader' }, `Serving now${r.optimization ? ' · ' + (OPTIMIZATIONS.find(o => o[0] === r.optimization)?.[1] || r.optimization) : ''}`),
    candidates.length
      ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm table-vcenter rank-table' },
        h('thead', {}, h('tr', {}, ['#', 'Model', 'Billing', 'Score', 'Why it is here'].map(x => h('th', {}, x)))),
        h('tbody', {}, candidates.map((c, i) => h('tr', {},
          h('td', { class: 'rank-num' }, i + 1),
          h('td', { class: 'mono' }, c.model),
          h('td', {}, badge(BILLING_TEXT[c.billing] || c.billing || '—', BILLING[c.billing] || 'secondary')),
          h('td', { class: 'rank-score' }, (c.score ?? 0).toFixed(3)),
          h('td', { class: 'text-secondary small' }, c.reason || ''))))))
      : h('div', { class: 'text-danger' }, 'Nothing available right now.'),
    p.evictions && Object.keys(p.evictions).length
      ? h('div', { class: 'form-hint' }, 'Out for now: ' + Object.entries(p.evictions).map(([m, t]) => `${m} (back in ${left(t) || 'a moment'})`).join(', '))
      : null);

  // The rejected models are grouped by the reason they were left out, so the
  // list stays short and the reason reads once.
  const reasons = new Map();
  for (const x of r.rejected || []) {
    const list = reasons.get(x.why) || [];
    list.push(x.model);
    reasons.set(x.why, list);
  }
  const rejected = reasons.size ? h('div', { class: 'mb-3' },
    h('div', { class: 'subheader' }, `Left out (${(r.rejected || []).length})`),
    h('div', { class: 'rank-reasons' }, [...reasons.entries()].sort((a, b) => b[1].length - a[1].length).map(([why, models]) => h('div', { class: 'rank-reason' },
      h('div', { class: 'rank-reason-head' }, h('span', { class: 'rank-reason-count' }, models.length), h('span', {}, why)),
      h('div', { class: 'rank-reason-models mono' }, models.join(', ')))))) : null;

  // ---- The model picker, grouped by vendor family ----
  // The catalog is fetched when the picker is first opened: a client page can
  // hold seven presets and the catalog is a few hundred rows.
  let lastRows = [];
  const picker = h('div', { class: 'picker' });
  const summary = h('span', { class: 'text-secondary small' }, `${draft.models.length} picked`);

  const pickerBody = () => {
    if (!lastRows.length) return [empty('kenari has no model of this kind.')];
    const families = new Map();
    for (const m of lastRows) {
      const list = families.get(m.owner || 'other') || [];
      list.push(m);
      families.set(m.owner || 'other', list);
    }
    const out = [];
    for (const [owner, list] of [...families.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      const ids = list.map(m => m.id);
      const box = h('input', { class: 'form-check-input', type: 'checkbox', onchange: e => {
        if (e.target.checked) { for (const id of ids) if (!draft.models.includes(id)) draft.models.push(id); }
        else draft.models = draft.models.filter(id => !ids.includes(id));
        redraw();
      } });
      const picked = ids.filter(id => draft.models.includes(id)).length;
      box.checked = picked === ids.length;
      box.indeterminate = picked > 0 && picked < ids.length;
      out.push(h('div', { class: 'picker-family' },
        h('label', { class: 'form-check picker-head' }, box,
          h('span', { class: 'form-check-label fw-bold' }, owner),
          h('span', { class: 'text-secondary small ms-2' }, `${picked}/${ids.length}`)),
        h('div', { class: 'picker-rows' }, list.map(m => {
          const on = draft.models.includes(m.id);
          const cb = h('input', { class: 'form-check-input', type: 'checkbox', checked: on, onchange: e => {
            if (e.target.checked) { if (!draft.models.includes(m.id)) draft.models.push(m.id); }
            else draft.models = draft.models.filter(id => id !== m.id);
            redraw();
          } });
          // free and plan-covered rows still show list prices, greyed, so the
          // pool can be compared on the same numbers
          const grey = m.billing !== 'balance';
          return h('label', { class: 'picker-row' + (on ? ' picked' : '') },
            cb,
            h('span', { class: 'picker-id mono' }, m.id),
            badge(BILLING_TEXT[m.billing] || m.billing || '—', BILLING[m.billing] || 'secondary'),
            h('span', { class: 'picker-ctx text-secondary nowrap' }, ctx(m.context) + ' ctx'),
            h('span', { class: 'picker-mod text-secondary nowrap' }, m.modalities || '—'),
            h('span', { class: 'picker-tool nowrap' }, m.tool_call ? badge('tools', 'azure') : h('span', { class: 'text-secondary small' }, 'no tools')),
            h('span', { class: 'picker-price nowrap' + (grey ? ' text-secondary' : '') }, priceLine(m)),
            m.promo_off > 0 ? badge(`promo −${Math.round(m.promo_off)}%`, 'orange') : null,
            !m.callable ? h('span', { class: 'text-orange small nowrap' }, 'no key may call it') : null);
        }))));
    }
    return out;
  };

  // redraw rebuilds the picker from draft.models: it is the single source of
  // truth, so a family box can never disagree with the rows under it.
  const redraw = () => {
    picker.replaceChildren(...pickerBody());
    summary.textContent = `${draft.models.length} of ${lastRows.length} picked`;
    sync();
  };

  let loaded = false;
  const load = async () => {
    if (loaded) return;
    loaded = true;
    picker.replaceChildren(empty('Loading the catalog…'));
    try {
      const res = await api(`catalog/models?client=${encodeURIComponent(cl.name)}&category=${encodeURIComponent(p.category)}`);
      lastRows = res.models || [];
      redraw();
    } catch (e) {
      loaded = false;
      picker.replaceChildren(h('div', { class: 'text-danger' }, e.message));
    }
  };

  const body = h('div', {},
    h('div', { class: 'row g-3 mb-3' },
      h('div', { class: 'col-md-5' }, h('label', { class: 'form-label' }, 'Optimization'), opt, optHelp),
      h('div', { class: 'col-md-7' }, h('label', { class: 'form-label' }, 'Pool'),
        h('div', { class: 'd-flex align-items-center gap-2' }, summary, h('span', { class: 'ms-auto' }), badgeDirty, saveBtn),
        h('div', { class: 'form-hint' }, 'Every model you tick is in the pool; the optimization only orders it. A family box selects all of that vendor\'s models here.'))),
    h('div', { class: 'mb-3' }, h('details', { class: 'picker-box', ontoggle: e => { if (e.target.open) load(); } },
      h('summary', {}, 'Choose models'), picker)),
    serving, rejected);

  return card(h('span', { class: 'd-flex align-items-center gap-2' }, h('span', { class: 'kind-icon' }, icon(ic)),
    h('span', {}, p.name, h('div', { class: 'text-secondary small fw-normal' }, `${p.label} · ${(p.models || []).length} in the pool · ${candidates.length} serving`))),
  body, {
    actions: h('div', { class: 'd-flex gap-1 align-items-center' },
      btn('Recalculate', 'btn-sm btn-ghost-secondary', () => act(api('recompute?client=' + encodeURIComponent(cl.name), { method: 'POST' }), 'Rankings recalculated'), 'adjustments-horizontal'),
      iconBtn('Delete preset', 'trash', () => { if (confirm(`Delete preset "${p.name}" of ${cl.name}? Apps asking for it get an error until you add it again.`)) act(api('presets', { method: 'POST', body: { client: cl.name, action: 'delete', preset: p.name } }), 'Preset deleted'); }, { sm: true, cls: 'btn-ghost-danger' })),
    class: 'h-100',
  });
}

function addPreset(client, missing) {
  const f = { category: missing[0].category };
  dialog(h('h3', {}, 'Add a preset'), h('p', { class: 'text-secondary' }, 'A client holds at most one preset per category. It starts empty and Balanced, so you only have to pick models.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Category'), h('select', { class: 'form-select', onchange: e => f.category = e.target.value },
      missing.map(c => h('option', { value: c.category }, `${c.preset} (${c.label})`)))),
    dialogButtons(btn('Add', 'btn-primary', async () => {
      try { await api('presets', { method: 'POST', body: { client, action: 'save', category: f.category, optimization: 'balanced', models: [] } }); closeDialog(); toast('Preset added'); render(); } catch (e) { fail(e); }
    })));
}

// ---------- Usage ----------
// Requirement 18: all clients, then one client, then its presets, then the
// models inside a preset. Every level is one request, so the page is a link:
// #/usage?client=babu&preset=text&window=24h
views.usage = async function (_, q) {
  const client = q.get('client') || '', preset = q.get('preset') || '', window = q.get('window') || '24h';
  const qs = new URLSearchParams({ window });
  if (client) qs.set('client', client);
  if (preset) qs.set('preset', preset);
  const d = await api('usage/drill?' + qs);
  const rows = d.rows || [], t = d.totals || {}, hours = window === '30d' ? 720 : 24;
  // at builds a link to another level of the same drill-down: the window is
  // kept, and a field the caller leaves out keeps its current value.
  const at = patch => {
    const n = new URLSearchParams({ window });
    const nextClient = patch.client === undefined ? client : patch.client;
    const nextPreset = patch.preset === undefined ? preset : patch.preset;
    if (nextClient) n.set('client', nextClient);
    if (nextPreset) n.set('preset', nextPreset);
    return 'usage?' + n;
  };
  const crumbs = [h('a', { class: 'clickable', onclick: () => go(at({ client: '', preset: '' })) }, 'All clients')];
  if (client) crumbs.push(' / ', preset ? h('a', { class: 'clickable', onclick: () => go(at({ preset: '' })) }, client) : client);
  if (preset) crumbs.push(' / ', preset);
  const level = d.level;
  const next = level === 'client' ? 'presets' : level === 'preset' ? 'models' : 'presets';
  setHead({
    crumbs, title: 'Usage', sub: `${window === '30d' ? 'Last 30 days' : 'Last 24 hours'}${client ? ' · ' + client : ''}${preset ? ' / ' + preset : ''}`,
    actions: [
      h('div', { class: 'btn-group' }, [['24h', '24 h'], ['30d', '30 d']].map(([v, l]) => {
        const n = new URLSearchParams({ window: v });
        if (client) n.set('client', client);
        if (preset) n.set('preset', preset);
        return h('button', { class: 'btn btn-sm ' + (window === v ? 'btn-primary' : 'btn-outline-primary'), onclick: () => go('usage?' + n) }, l);
      })),
      level !== 'client' && btn('Back', 'btn-outline-secondary', () => go(at(level === 'model' ? { preset: '' } : { client: '', preset: '' })), 'arrow-left'),
    ],
  });

  const k = d.kenari || {};
  const wins = k.windows || [];
  const acct = k.account || {};
  const strip = card('kenari: plan and balance', k.configured
    ? h('div', { class: 'row g-3' },
      h('div', { class: 'col-lg-6' }, h('div', { class: 'd-flex align-items-center flex-wrap mb-2' },
        h('strong', { class: 'me-2' }, acct.plan || 'No plan'), acct.plan_source === 'kenari' ? badge('live from kenari', 'green') : badge('estimated', 'orange'))),
      h('div', { class: 'col-lg-6' }, h('div', { class: 'd-flex justify-content-between' }, h('span', { class: 'text-secondary' }, 'Balance'), h('strong', {}, 'Rp ' + idr(acct.balance_idr || 0)))),
      wins.length
        ? wins.map(w => h('div', { class: 'col-lg-4' }, h('div', { class: 'd-flex justify-content-between small mb-1' }, h('span', {}, w.label), h('span', { class: 'text-secondary' }, `Rp ${idr(w.used_idr)} of ${idr(w.budget_idr)}`)),
          progress(w.used_idr, w.budget_idr), w.resets_at && !zero(w.resets_at) && h('div', { class: 'text-secondary small mt-1' }, 'Resets in ' + (left(w.resets_at) || 'a moment'))))
        : h('div', { class: 'col-12' }, h('div', { class: 'text-secondary small' }, 'kenari has not reported the plan windows yet; add the account key on the Overview page.')))
    : empty('No kenari key yet. Add one to a client to read plan and balance.'));

  const cols = ['Name', 'Requests', 'Errors', 'Tokens in / out', 'Cached', 'RTK saved', 'Rp at list price'];
  const table = rows.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table table-hover' },
    h('thead', {}, h('tr', {}, cols.map(x => h('th', {}, x)))),
    h('tbody', {}, rows.map(r => {
      const drill = level === 'client' ? () => go(at({ client: r.client || r.key, preset: '' }))
        : level === 'preset' ? () => go(at({ preset: r.preset || r.key })) : null;
      return h('tr', { class: drill ? 'clickable' : '', onclick: drill || undefined },
        h('td', {}, h('span', { class: 'mono' }, r.label || r.key), r.billing && badge(BILLING_TEXT[r.billing] || r.billing, BILLING[r.billing] || 'secondary')),
        h('td', {}, num(r.requests)),
        h('td', { class: r.errors ? 'text-danger' : 'text-secondary' }, num(r.errors)),
        h('td', { class: 'nowrap' }, `${num(r.in_tok)} / ${num(r.out_tok)}`),
        h('td', { class: 'text-secondary' }, num(r.cached_tok)),
        h('td', { class: 'text-secondary' }, num(r.rtk_saved)),
        h('td', { class: 'nowrap' }, 'Rp ' + idr(r.cost_idr)),
        drill && h('td', { class: 'text-end' }, icon('chevron-right', 'text-secondary')));
    })))) : empty(level === 'client' ? 'No traffic in this period.' : 'Nothing served here in this period.');

  return h('div', {},
    statRow([
      ['bolt', 'blue', num(t.requests || 0), `requests · ${num(t.in_tok || 0)} in / ${num(t.out_tok || 0)} out tokens`],
      ['alert-triangle', t.errors ? 'orange' : 'green', t.requests ? (t.errors / t.requests * 100).toFixed(1) + '%' : '0%', `failed (${num(t.errors || 0)} of ${num(t.requests || 0)})`],
      ['wallet', 'green', num(Math.round((t.rtk_saved || 0) / 1000)) + ' k', 'characters saved by RTK'],
      ['coin', 'azure', 'Rp ' + idr(t.cost_idr || 0), 'at list price, free and plan included']]),
    h('div', { class: 'mb-3' }, card('Requests over time', chart(d.series || [], hours, { unit: hours > 72 ? 'day' : 'hour' }))),
    h('div', { class: 'mb-3' }, strip),
    card(level === 'client' ? 'Clients' : level === 'preset' ? `Presets of ${client}` : `Models in ${preset}`,
      h('div', {}, h('div', { class: 'text-secondary small mb-2' }, level === 'model' ? 'Click a row to go back.' : `Click a row to open its ${next}.`), table), { flush: true }));
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
$('#logoutall').onclick = () => { if (confirm('End every signed-in session?')) api('logout-all', { method: 'POST' }).then(() => location.reload(), fail); };
$('#theme').onclick = () => { const t = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark'; document.documentElement.setAttribute('data-bs-theme', t); try { localStorage.setItem('xl-theme', t); } catch (e) { /* storage may be blocked */ } };
$('#loginform').addEventListener('submit', async e => {
  e.preventDefault(); const f = new FormData(e.target); $('#loginerr').textContent = '';
  try { await api('login', { method: 'POST', body: { username: f.get('username'), password: f.get('password'), code: f.get('code') } }); e.target.reset(); showApp(); render(); }
  catch (err) { $('#loginerr').textContent = err.message; }
});
window.addEventListener('hashchange', () => { if (S.dirty) { S.dirty = false; $('#savebar').hidden = true; } S.cfg = null; render(); });
window.addEventListener('beforeunload', e => { if (S.dirty) { e.preventDefault(); e.returnValue = ''; } });
api('me').then(() => { showApp(); render(); }, () => showLogin());
