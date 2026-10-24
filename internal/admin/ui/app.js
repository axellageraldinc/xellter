/* xellter admin panel. Plain JS on top of Tabler (vendor/tabler.min.css); talks to /admin/api/*. */
'use strict';

const $ = (s, e = document) => e.querySelector(s);
const S = { cfg: null, yaml: '', history: [], tab: 'overview', dirty: false, timer: null };
const TABS = [['overview', 'Overview'], ['presets', 'Presets'], ['keys', 'Kenari keys'], ['clients', 'Clients'], ['usage', 'Usage'], ['settings', 'Settings'], ['config', 'Config file']];

// ---------- tiny DOM + API helpers ----------
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked' || k === 'selected' || k === 'disabled' || k === 'readonly') el[k === 'readonly' ? 'readOnly' : k] = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

async function api(path, opt = {}) {
  const r = await fetch('/admin/api/' + path, {
    method: opt.method || 'GET', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Xellter': '1' },
    body: opt.body === undefined ? undefined : JSON.stringify(opt.body),
  });
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

const idr = n => n == null ? '–' : n === 0 ? '0' : n >= 100 ? Math.round(n).toLocaleString('en-US') : n >= 1 ? n.toFixed(2) : n.toFixed(4);
const num = n => (n || 0).toLocaleString('en-US');
const human = s => s < 90 ? s + ' s' : s < 5400 ? Math.round(s / 60) + ' min' : s < 172800 ? Math.round(s / 3600) + ' h' : Math.round(s / 86400) + ' d';
const ago = t => human(Math.max(0, Math.round((Date.now() - new Date(t)) / 1000)));
const left = t => { const s = Math.round((new Date(t) - Date.now()) / 1000); return s <= 0 ? '' : human(s); };
const zero = t => !t || String(t).startsWith('0001');

const badge = (text, color = 'secondary') => h('span', { class: `badge bg-${color}-lt me-1 mb-1` }, text);
const dot = color => h('span', { class: `status-dot bg-${color} me-2` });
const progress = (used, total, big) => {
  const p = total > 0 ? Math.min(100, used / total * 100) : 0;
  return h('div', { class: 'progress' + (big ? '' : ' progress-sm') }, h('div', { class: 'progress-bar ' + (p >= 97 ? 'bg-danger' : p >= 80 ? 'bg-warning' : 'bg-primary'), style: `width:${p}%`, role: 'progressbar' }));
};
const card = (title, body, o = {}) => h('div', { class: 'card ' + (o.class || '') },
  (title || o.actions) && h('div', { class: 'card-header' }, h('h3', { class: 'card-title' }, title), o.actions && h('div', { class: 'card-actions' }, o.actions)),
  h('div', { class: 'card-body' + (o.flush ? ' p-0' : '') }, body));
const pageHeader = (title, sub, actions) => h('div', { class: 'd-flex flex-wrap align-items-center gap-2 mb-3' }, h('div', { class: 'me-auto', style: 'max-width:46rem' }, h('h2', { class: 'page-title mb-0' }, title), sub && h('div', { class: 'text-secondary' }, sub)), actions);
const empty = text => h('div', { class: 'text-secondary py-3' }, text);
const promoLabel = f => f.startsWith('promo') ? f.split(' until')[0] : f;
const flagBadges = flags => (flags || []).filter(f => /^(free|plan|promo)/.test(f)).map(f => badge(promoLabel(f), f === 'free' ? 'green' : f === 'plan' ? 'blue' : 'orange'));

// ---------- form helpers: they edit S.cfg in place and mark it dirty ----------
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
  S.cfg.upstreams ||= []; S.cfg.clients ||= []; S.cfg.presets ||= {}; S.cfg.benchmarks ||= {};
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
const dialogButtons = (...btns) => h('div', { class: 'd-flex gap-2 justify-content-end mt-3' }, h('button', { class: 'btn btn-ghost-secondary', onclick: closeDialog }, 'Close'), btns);

const views = {};
const KIND = { chat: 'Chat', images: 'Images', videos: 'Video', audio_speech: 'Speech', audio_transcription: 'Transcription', music: 'Music' };
const MODALITIES = { chat: 'Chat', images: 'Image generation', videos: 'Video generation', audio_speech: 'Text to speech', audio_transcription: 'Speech to text', music: 'Music' };

// ---------- Overview ----------
views.overview = async function (root) {
  const [o, hours0] = await Promise.all([api('overview'), api('usage?group=hour&hours=24')]);
  const hours = hours0 || [];
  const tot = hours.reduce((a, r) => ({ req: a.req + r.requests, err: a.err + r.errors, cost: a.cost + r.cost_idr, tok: a.tok + r.in_tok + r.out_tok }), { req: 0, err: 0, cost: 0, tok: 0 });
  const stat = (label, value, sub) => h('div', { class: 'col-6 col-lg-3' }, h('div', { class: 'card' }, h('div', { class: 'card-body' }, h('div', { class: 'subheader' }, label), h('div', { class: 'h1 mb-0' }, value), sub && h('div', { class: 'text-secondary small' }, sub))));
  const lc = { info: 'azure', warn: 'orange', bad: 'red' };
  root.replaceChildren(
    pageHeader('Overview', `Running for ${human(o.uptime_s)} · ${o.catalog?.models || 0} models in the catalog, refreshed ${o.catalog?.age_s ?? '?'} s ago`,
      h('button', { class: 'btn btn-outline-secondary btn-sm', onclick: () => act(api('catalog/refresh', { method: 'POST' }), 'Catalog refreshed') }, 'Refresh catalog')),
    h('div', { class: 'mb-3' }, card(null, h('div', {},
      h('div', { class: 'd-flex align-items-center' }, dot(o.status === 'bad' ? 'red' : o.status === 'warn' ? 'orange' : 'green'),
        h('strong', {}, o.status === 'ok' ? 'Everything is running normally' : o.status === 'bad' ? 'Something needs your attention' : 'Running, with warnings')),
      (o.alerts || []).map(a => h('div', { class: 'alert-row' }, badge(a.level === 'info' ? 'tip' : a.level, lc[a.level]), h('div', { class: 'me-auto' }, a.text), a.go && h('a', { class: 'clickable text-nowrap', onclick: () => go(a.go) }, 'Open →')))))),
    h('div', { class: 'row row-cards mb-3' },
      stat('Requests, last 24 h', num(tot.req), `${num(tot.tok)} tokens`),
      stat('Errors', tot.req ? (tot.err / tot.req * 100).toFixed(1) + '%' : '0%', `${num(tot.err)} failed`),
      stat('Cost at list price', 'Rp ' + idr(tot.cost), 'free and plan traffic included'),
      stat('Saved by token saver', num(Math.round(o.requests.rtk_saved_chars / 1000)) + ' k', 'characters, since start')),
    h('div', { class: 'row row-cards mb-3' },
      h('div', { class: 'col-lg-5' }, card('Kenari plan & balance', (o.accounts || []).length ? o.accounts.map(planBlock) : empty('No kenari key yet. Add one under "Kenari keys".'), { class: 'h-100' })),
      h('div', { class: 'col-lg-7' }, card('What each preset serves right now', presetTable(o.presets || []), { flush: true, class: 'h-100', actions: h('a', { class: 'clickable', onclick: () => go('presets') }, 'Edit →') }))),
    card('Kenari keys', keyTable(o.keys || []), { flush: true, actions: h('a', { class: 'clickable', onclick: () => go('keys') }, 'Manage →') }));
  root._refresh = 10000;
};

function planBlock(ac) {
  const wins = ac.windows ? Object.entries(ac.windows).map(([n, w]) => ({ n, used: w.used_rp, budget: w.budget_rp, reset: w.resets_at })) :
    ['5h', 'week', 'month'].map((n, i) => ({ n, used: ac.estimated_spend[i], budget: ac.plan_budget[i] })).filter(w => w.budget > 0);
  const names = { five_hour: '5-hour window', '5h': '5-hour window', week: 'This week', month: 'This month' };
  return h('div', { class: 'mb-3' },
    h('div', { class: 'd-flex align-items-center flex-wrap mb-2' }, h('strong', { class: 'me-2' }, ac.plan ? `${ac.plan} plan` : 'No plan'), badge(ac.tier), ac.plan && badge(ac.plan_source === 'kenari' ? 'live from kenari' : 'estimated', ac.plan_source === 'kenari' ? 'green' : 'orange')),
    wins.map(w => h('div', { class: 'mb-2' }, h('div', { class: 'd-flex justify-content-between small flex-wrap' }, h('span', {}, names[w.n] || w.n), h('span', { class: 'text-secondary' }, `Rp ${idr(w.used)} of ${idr(w.budget)}${w.reset && !zero(w.reset) ? ' · resets in ' + (left(w.reset) || '–') : ''}`)), progress(w.used, w.budget))),
    !wins.length && h('div', { class: 'text-secondary small mb-2' }, 'No plan budget to show.'),
    h('div', { class: 'small text-secondary' }, `Requests this minute ${ac.rpm_used}/${ac.rpm_limit || '∞'}`, ac.daily_limit > 0 ? ` · free requests today ${ac.daily_used}/${ac.daily_limit}` : '', !zero(ac.balance_at) ? ` · balance Rp ${idr(ac.balance_idr)}` : ''));
}

function presetTable(rows) {
  return h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table' },
    h('thead', {}, h('tr', {}, ['Preset', 'Serving now', 'Next in line'].map(x => h('th', {}, x)))),
    h('tbody', {}, rows.map(p => h('tr', {}, h('td', {}, h('strong', {}, p.name), h('div', { class: 'text-secondary small' }, KIND[p.modality] || p.modality)),
      h('td', {}, p.now ? [h('span', { class: 'mono' }, p.now), ' ', flagBadges(p.flags)] : badge('nothing available', 'red')),
      h('td', { class: 'text-secondary mono small' }, (p.next || []).join(', ') || '–'))))));
}

function keyTable(keys) {
  return h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table' },
    h('thead', {}, h('tr', {}, ['Key', 'Account', 'Models it can use', 'State'].map(x => h('th', {}, x)))),
    h('tbody', {}, keys.map(k => {
      const paused = (!zero(k.cooldown_until) && left(k.cooldown_until)) || (!zero(k.breaker_until) && left(k.breaker_until));
      return h('tr', {}, h('td', {}, h('strong', {}, k.name), ' ', k.unrestricted && badge('full access', 'green')), h('td', {}, k.account),
        h('td', {}, k.learned_allow?.length ? `${k.learned_allow.length} model${k.learned_allow.length > 1 ? 's' : ''}` : 'all models'),
        h('td', {}, !k.enabled ? badge(k.unrestricted ? 'reads plan & balance only' : 'disabled', k.unrestricted ? 'green' : 'secondary') : !k.has_secret ? badge('no key value', 'red') : paused ? badge('paused ' + paused, 'orange') : badge('ready', 'green'), k.last_error && h('div', { class: 'text-secondary small' }, k.last_error)));
    }))));
}

// ---------- Presets ----------
const OBJECTIVES = {
  cheapest: { label: 'Cheapest', w: { cost: 60, benchmark: 10, jev: 10, reliability: 15, context: 5 }, q: 0.30 },
  balanced: { label: 'Balanced', w: { cost: 30, benchmark: 25, jev: 20, reliability: 15, context: 10 }, q: 0.45 },
  quality: { label: 'Best quality', w: { cost: 10, benchmark: 35, jev: 30, reliability: 15, context: 10 }, q: 0.60 },
};
const objectiveOf = p => Object.entries(OBJECTIVES).find(([, o]) => Object.entries(o.w).every(([k, v]) => (p.weights || {})[k] === v))?.[0] || 'custom';

views.presets = async function (root) {
  await ensureCfg();
  const rk = await api('ranking');
  const grid = h('div', { class: 'row row-cards' });
  for (const name of Object.keys(S.cfg.presets).sort()) {
    const p = S.cfg.presets[name]; p.require ||= {}; p.weights ||= {}; p.cost_profile ||= {};
    const r = rk[name], isChat = p.modality === 'chat', obj = objectiveOf(p);
    const body = h('div', {},
      h('div', { class: 'text-secondary mb-3' }, p.description || 'No description'),
      h('div', { class: 'mb-3' }, h('div', { class: 'subheader mb-1' }, 'Serving now'),
        r?.candidates?.length ? r.candidates.slice(0, 3).map((c, i) => h('div', { class: 'mb-1' }, h('span', { class: 'mono' + (i ? ' text-secondary' : ' fw-bold') }, c.model), ' ', flagBadges(c.flags))) : badge('nothing available right now', 'red'),
        r?.trial?.length ? h('div', { class: 'text-secondary small' }, `${r.trial.length} new model(s) on trial`) : null),
      isChat ? h('div', { class: 'mb-3' }, h('div', { class: 'subheader mb-1' }, 'Optimise for'),
        h('div', { class: 'btn-group w-100' }, Object.entries(OBJECTIVES).map(([k, o]) => h('button', { class: 'btn btn-sm ' + (obj === k ? 'btn-primary' : 'btn-outline-primary'), onclick: () => { p.weights = { ...o.w }; p.min_quality = o.q; markDirty(); render(); } }, o.label)), obj === 'custom' && h('button', { class: 'btn btn-sm btn-primary', disabled: true }, 'Custom')),
        obj === 'custom' && h('div', { class: 'form-hint' }, 'Tuned by hand (see Advanced). Pick one of the options to reset it.')) : h('div', { class: 'text-secondary small mb-3' }, 'Always the cheapest working model first; free models come before paid ones.'),
      h('div', { class: 'mb-3' }, h('div', { class: 'subheader mb-1' }, 'When several models are equally good'),
        h('select', { class: 'form-select form-select-sm', onchange: e => { p.strategy = e.target.value; markDirty(); } }, h('option', { value: 'priority', selected: p.strategy === 'priority' }, 'Always use the best one'), h('option', { value: 'round_robin', selected: p.strategy === 'round_robin' }, 'Take turns between them'))),
      isChat && h('div', { class: 'mb-2' }, h('div', { class: 'subheader mb-1' }, 'The model must support'),
        field(p.require, 'tool_call', { type: 'switch', label: 'Tool calling (agents, function calls)' }), field(p.require, 'vision', { type: 'switch', label: 'Image input' }),
        field(p.require, 'min_context', { type: 'select', label: 'Context size', cast: Number, custom: v => `${Math.round(v / 1000)}k tokens (custom)`, opts: [[0, 'Any'], [32000, 'at least 32k tokens'], [64000, 'at least 64k tokens'], [128000, 'at least 128k tokens'], [256000, 'at least 256k tokens']] })),
      advanced(cols(
        field(p, 'description'), field(p, 'modality', { type: 'select', label: 'Kind', opts: Object.entries(MODALITIES) }), field(p, 'mode', { type: 'select', label: 'How it picks', opts: [['auto', 'Automatically (ranking)'], ['combo', 'Fixed list, in order']] }),
        field(p, 'models', { type: 'list', label: 'Fixed list of models', help: 'only used for "Fixed list"' }), field(p, 'allow', { type: 'list', label: 'Only consider models matching', ph: 'e.g. deepseek-*, glm-*' }), field(p, 'deny', { type: 'list', label: 'Never use models matching' }),
        field(p, 'min_quality', { type: 'num', step: '0.01', label: 'Minimum quality (0-1)' }), field(p, 'max_candidates', { type: 'num', label: 'How many models to keep' }), field(p, 'rr_band', { type: 'num', step: '0.01', label: '"Equally good" means within' }),
        ['cost', 'benchmark', 'jev', 'reliability', 'context'].map(k => field(p.weights, k, { type: 'num', label: `Weight: ${k}` })),
        [['input_tokens', 'Typical input tokens'], ['output_tokens', 'Typical output tokens'], ['cached_ratio', 'Share of input served from cache'], ['images', 'Images per request'], ['seconds', 'Seconds of audio/video'], ['chars', 'Characters of speech']].map(([k, l]) => field(p.cost_profile, k, { type: 'num', label: l }))),
        h('button', { class: 'btn btn-outline-danger btn-sm', onclick: () => { if (confirm(`Delete preset "${name}"? Apps calling it will get an error.`)) { delete S.cfg.presets[name]; markDirty(); render(); } } }, 'Delete this preset')));
    grid.append(h('div', { class: 'col-md-6 col-xl-4' }, card(h('span', {}, name, ' ', badge(KIND[p.modality] || p.modality, 'azure')), body,
      { actions: h('button', { class: 'btn btn-sm btn-ghost-secondary', onclick: () => showRanking(name) }, 'Why this order?'), class: 'h-100' })));
  }
  root.replaceChildren(pageHeader('Presets', 'A preset is a name your apps use like a model, for example "balanced". For every request xellter picks the real model behind it, using live prices, free tiers, promos and quality.',
    h('button', { class: 'btn btn-primary', onclick: newPreset }, '+ New preset')), grid);
};

function newPreset() {
  const f = { name: '', from: 'balanced' };
  dialog(h('h3', {}, 'New preset'), h('p', { class: 'text-secondary' }, 'Name it after what it is for (for example "research" or "summaries"). Apps use that name as the model.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'research', oninput: e => f.name = e.target.value.trim() })),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Start from'), h('select', { class: 'form-select', onchange: e => f.from = e.target.value }, Object.keys(S.cfg.presets).sort().map(n => h('option', { value: n, selected: n === f.from }, n)))),
    dialogButtons(h('button', { class: 'btn btn-primary', onclick: () => {
      if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$/.test(f.name)) return toast('Use letters, digits, dot, dash or underscore', true);
      if (S.cfg.presets[f.name]) return toast('That name is already used', true);
      S.cfg.presets[f.name] = JSON.parse(JSON.stringify(S.cfg.presets[f.from])); markDirty(); closeDialog(); render();
    } }, 'Create')));
}

async function showRanking(name) {
  try {
    const r = await api('ranking?preset=' + encodeURIComponent(name));
    const row = (c, i) => h('tr', {}, h('td', {}, i + 1), h('td', { class: 'mono' }, c.model), h('td', {}, c.score.toFixed(2)), h('td', { class: 'nowrap' }, c.cost_idr ? 'Rp ' + idr(c.cost_idr) : 'free / plan'),
      h('td', {}, (c.flags || []).map(f => badge(f, /free|plan|promo|proven/.test(f) ? 'green' : 'orange'))), h('td', { class: 'text-secondary small' }, Object.entries(c.components || {}).map(([k, v]) => `${k} ${v.toFixed(2)}`).join(' · ')));
    const tbl = list => h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm' }, h('thead', {}, h('tr', {}, ['#', 'Model', 'Score', 'Cost per typical request', 'Why', 'Scores'].map(x => h('th', {}, x)))), h('tbody', {}, list.map(row))));
    dialog(h('h3', {}, `${name}: how the order is decided`), h('p', { class: 'text-secondary' }, 'Higher score wins. Models close to the top score take turns when the preset is set to do so.'),
      r.candidates?.length ? tbl(r.candidates) : h('p', { class: 'text-danger' }, 'No model qualifies right now.'),
      r.trial?.length ? [h('h4', {}, 'New models on trial'), h('p', { class: 'text-secondary' }, 'They get a small share of traffic until they have proven themselves.'), tbl(r.trial)] : null,
      r.rejected?.length ? [h('h4', {}, `Left out (${r.rejected.length})`), h('table', { class: 'table table-sm' }, h('tbody', {}, r.rejected.map(x => h('tr', {}, h('td', { class: 'mono' }, x.model), h('td', { class: 'text-secondary' }, x.why)))))] : null,
      r.unavailable?.length ? h('p', { class: 'text-secondary small' }, `${r.unavailable.length} more models exist but none of your keys may call them.`) : null, dialogButtons());
  } catch (e) { fail(e); }
}

// ---------- Kenari keys ----------
views.keys = async function (root) {
  await ensureCfg();
  const o = await api('overview');
  const st = Object.fromEntries((o.keys || []).map(k => [k.name, k]));
  const planNames = o.plan_names || [];
  const acctName = u => u.account || 'main';
  const kids = [pageHeader('Kenari keys', 'The API keys xellter uses to reach kenari.id. Keys of one kenari account share its limits and plan.', h('button', { class: 'btn btn-primary', onclick: addKey }, '+ Add key'))];
  if (!(o.accounts || []).some(a => a.quota_key)) kids.push(h('div', { class: 'alert alert-info' }, h('strong', {}, 'Tip: '), 'add one full-access key (a key without model or spend limits). With it xellter reads your real plan, usage, reset times and balance from kenari, and can use every model. Keys with limits work too, but plan numbers are then only estimates.'));
  for (const ac of o.accounts || []) {
    const members = S.cfg.upstreams.filter(u => acctName(u) === ac.name);
    const cur = members.find(u => u.plan)?.plan || '';
    kids.push(h('div', { class: 'mb-3' }, card(`Account "${ac.name}"`, h('div', { class: 'row g-3' },
      h('div', { class: 'col-lg-6' }, planBlock(ac)),
      h('div', { class: 'col-lg-6' }, h('label', { class: 'form-label' }, 'Your kenari plan'),
        ac.plan_source === 'kenari' ? h('div', {}, badge(ac.plan || 'no plan', 'green'), h('div', { class: 'form-hint' }, 'Read from kenari with a full-access key. Nothing to set.')) :
          [h('select', { class: 'form-select', onchange: e => { members.forEach(u => { u.plan = e.target.value; }); markDirty(); } }, h('option', { value: '' }, 'I have no plan / not sure'), planNames.map(n => h('option', { value: n, selected: n === cur }, n))),
            h('div', { class: 'form-hint' }, 'xellter cannot see this with limited keys, so tell it which plan you have.')])))));
  }
  const list = h('div', { class: 'row row-cards' });
  S.cfg.upstreams.forEach(u => {
    const s = st[u.name] || {};
    const paused = (!zero(s.cooldown_until) && left(s.cooldown_until)) || (!zero(s.breaker_until) && left(s.breaker_until));
    list.append(h('div', { class: 'col-lg-6' }, card(h('span', {}, u.name, ' ', !u.enabled ? badge(s.unrestricted ? 'reads plan & balance only' : 'disabled', s.unrestricted ? 'green' : 'secondary') : !s.has_secret ? badge('no key value', 'red') : paused ? badge('paused ' + paused, 'orange') : badge('ready', 'green'), s.unrestricted && badge('full access', 'green')),
      h('div', {},
        h('div', { class: 'mb-2' }, s.learned_allow?.length ? ['Can use ', h('strong', {}, s.learned_allow.length + (s.learned_allow.length > 1 ? ' models' : ' model')), h('div', { class: 'text-secondary small mono' }, s.learned_allow.join(', '))] : 'Can use every model'),
        s.last_error && h('div', { class: 'text-danger small mb-2' }, 'Last error: ' + s.last_error),
        (s.blocked_models || []).length > 0 && h('div', { class: 'mb-2 small' }, 'Temporarily skipping: ', s.blocked_models.map(m => badge(m, 'orange'))),
        h('div', { class: 'd-flex flex-wrap gap-2' },
          h('button', { class: 'btn btn-sm btn-outline-primary', onclick: () => replaceKey(u) }, 'Replace key'),
          h('button', { class: 'btn btn-sm btn-outline-secondary', onclick: () => act(api('keys/probe', { method: 'POST', body: {} }), 'Checked models and plan again') }, 'Re-check'),
          h('button', { class: 'btn btn-sm btn-outline-secondary', onclick: () => act(api('keys/reset', { method: 'POST', body: { name: u.name } }), 'Pauses cleared') }, 'Clear pauses'),
          h('button', { class: 'btn btn-sm btn-outline-danger ms-auto', onclick: () => { if (confirm(`Remove key "${u.name}" from xellter? (It stays valid at kenari.)`)) act(api('upstreams', { method: 'POST', body: { action: 'delete', name: u.name } }), 'Key removed'); } }, 'Remove')),
        advanced(cols(field(u, 'enabled', { type: 'switch', label: 'Use this key' }), field(u, 'account', { label: 'Account', ph: 'main', help: 'only change if you have more than one kenari account' }),
          field(u, 'tier', { type: 'select', label: 'Account tier', opts: [['auto', 'Detect'], ['free', 'Free'], ['topup', 'Topped up'], ['plan', 'Has a plan']] }),
          field(u, 'rpm', { type: 'num', label: 'Requests per minute (0 = kenari default)' }), field(u, 'daily_free', { type: 'num', label: 'Free requests per day (0 = default, -1 = unlimited)' }),
          field(u, 'allow', { type: 'list', label: 'Only use these models', ph: 'normally detected automatically' }), field(u, 'note', { label: 'Note' })))))));
  });
  kids.push(list);
  root.replaceChildren(...kids);
};

function addKey() {
  const f = { name: '', key: '' };
  dialog(h('h3', {}, 'Add a kenari key'), h('p', { class: 'text-secondary' }, 'Paste a key from the kenari dashboard. It is stored on this server only (mode 0600), never in the config file or the repo. xellter then checks which models it may use.'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'e.g. main, spare', oninput: e => f.name = e.target.value.trim() })),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'API key'), h('input', { class: 'form-control', type: 'password', placeholder: 'kn-…', autocomplete: 'off', oninput: e => f.key = e.target.value })),
    dialogButtons(h('button', { class: 'btn btn-primary', onclick: async () => { try { await api('upstreams', { method: 'POST', body: { action: 'create', name: f.name, key: f.key } }); closeDialog(); toast('Key added, checking it…'); S.cfg = null; render(); } catch (e) { fail(e); } } }, 'Add key')));
}

function replaceKey(u) {
  const f = { key: '' };
  dialog(h('h3', {}, `Replace the key of "${u.name}"`), h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'New API key'), h('input', { class: 'form-control', type: 'password', autocomplete: 'off', oninput: e => f.key = e.target.value })),
    dialogButtons(h('button', { class: 'btn btn-primary', onclick: async () => { try { await api('secret', { method: 'PUT', body: { name: u.key_env, value: f.key } }); closeDialog(); toast('Key replaced'); render(); } catch (e) { fail(e); } } }, 'Replace')));
}

// ---------- Clients ----------
views.clients = async function (root) {
  await ensureCfg();
  const o = await api('overview');
  const spent = Object.fromEntries((o.clients || []).map(c => [c.name, c.spent_today]));
  root.replaceChildren(pageHeader('Clients', 'The apps that call xellter. Each gets its own key and its own list of allowed presets. A key cannot be shared between clients.', h('button', { class: 'btn btn-primary', onclick: newClient }, '+ New client')),
    card(null, S.cfg.clients.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter card-table' },
      h('thead', {}, h('tr', {}, ['Client', 'Key', 'Allowed presets', 'Spent today', ''].map(x => h('th', {}, x)))),
      h('tbody', {}, S.cfg.clients.map(c => h('tr', {},
        h('td', {}, h('strong', {}, c.name), ' ', !c.enabled && badge('disabled', 'red'), c.note && h('div', { class: 'text-secondary small' }, c.note)),
        h('td', { class: 'mono' }, c.key_hint || '–'),
        h('td', {}, (c.presets || []).includes('*') ? badge('all presets', 'blue') : (c.presets || []).map(p => badge(p, 'blue')), (c.passthrough || []).length > 0 && badge('+ direct models', 'azure')),
        h('td', {}, 'Rp ' + idr(spent[c.name] || 0), c.daily_budget_idr > 0 && h('div', { class: 'text-secondary small' }, `limit Rp ${idr(c.daily_budget_idr)} a day`)),
        h('td', { class: 'text-end nowrap' }, h('button', { class: 'btn btn-sm btn-outline-secondary me-1', onclick: () => editClient(c) }, 'Edit'),
          h('button', { class: 'btn btn-sm btn-outline-secondary me-1', onclick: () => { if (confirm(`Create a new key for ${c.name}? The old key stops working immediately.`)) rotate(c); } }, 'New key'),
          h('button', { class: 'btn btn-sm btn-outline-danger', onclick: () => { if (confirm(`Delete client ${c.name}?`)) act(api('clients', { method: 'POST', body: { action: 'delete', name: c.name } }), 'Client deleted'); } }, 'Delete')))))), { flush: true }) : empty('No clients yet.')));
};

async function rotate(c) {
  try { const r = await api('clients', { method: 'POST', body: { action: 'rotate', name: c.name } }); S.cfg = null; showKey(c.name, r.key); render(); } catch (e) { fail(e); }
}

function showKey(name, key) {
  dialog(h('h3', {}, `API key for ${name}`), h('p', { class: 'text-secondary' }, `Copy it now: it is not stored and cannot be shown again. Use it as the bearer token (or x-api-key), with base URL ${location.origin}/v1`), h('div', { class: 'keybox mono' }, key), dialogButtons());
}

function presetChecks(sel, onChange) {
  const names = Object.keys(S.cfg.presets).sort(), all = sel.includes('*');
  return h('div', {}, h('label', { class: 'form-check' }, h('input', { class: 'form-check-input', type: 'checkbox', checked: all, onchange: e => onChange(e.target.checked ? ['*'] : []) }), h('span', { class: 'form-check-label' }, 'All presets (including future ones)')),
    h('div', { class: 'row' }, names.map(n => h('div', { class: 'col-6' }, h('label', { class: 'form-check' }, h('input', { class: 'form-check-input', type: 'checkbox', checked: all || sel.includes(n), disabled: all, onchange: e => { const s = new Set(sel); e.target.checked ? s.add(n) : s.delete(n); onChange([...s]); } }), h('span', { class: 'form-check-label' }, n))))));
}

function newClient() {
  if (S.dirty) return toast('Save or discard your pending changes first', true);
  const c = { name: '', presets: ['*'], passthrough: [], upstreams: [], daily_budget_idr: 0, note: '' };
  const holder = h('div'), draw = () => holder.replaceChildren(presetChecks(c.presets, v => { c.presets = v; draw(); }));
  draw();
  dialog(h('h3', {}, 'New client'),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Name'), h('input', { class: 'form-control', placeholder: 'e.g. my-app', oninput: e => c.name = e.target.value.trim() })),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Which presets may it use?'), holder),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Daily spending limit (Rp, 0 = none)'), h('input', { class: 'form-control', type: 'number', value: 0, onchange: e => c.daily_budget_idr = Number(e.target.value) })),
    dialogButtons(h('button', { class: 'btn btn-primary', onclick: async () => { try { const r = await api('clients', { method: 'POST', body: { action: 'create', client: c } }); S.cfg = null; showKey(c.name, r.key); render(); } catch (e) { fail(e); } } }, 'Create client')));
}

function editClient(c) {
  const holder = h('div'), draw = () => holder.replaceChildren(presetChecks(c.presets || [], v => { c.presets = v; markDirty(); draw(); }));
  draw();
  dialog(h('h3', {}, `Edit ${c.name}`), field(c, 'enabled', { type: 'switch', label: 'Allowed to call xellter' }),
    h('div', { class: 'mb-3' }, h('label', { class: 'form-label' }, 'Which presets may it use?'), holder),
    field(c, 'daily_budget_idr', { type: 'num', label: 'Daily spending limit (Rp, 0 = none)', help: 'counted at list price, plan and free traffic included' }), field(c, 'note', { label: 'Note' }),
    advanced(field(c, 'passthrough', { type: 'list', label: 'Real models it may call directly', ph: 'e.g. deepseek-v4-1-flash, nano-banana-pro', help: 'normally empty: apps should call presets' }),
      field(c, 'upstreams', { type: 'list', label: 'Only use these keys', ph: 'empty = all keys' }), field(c, 'rtk', { type: 'optswitch', label: 'Token saver (RTK)' })),
    dialogButtons(h('button', { class: 'btn btn-primary', onclick: async () => { const d = $('#dlg'); d.dataset.saving = '1'; closeDialog(); await saveCfg(); } }, 'Save')));
  $('#dlg').addEventListener('close', () => { if (S.dirty && !$('#dlg').dataset.saving) { S.cfg = null; S.dirty = false; $('#savebar').hidden = true; render(); } delete $('#dlg').dataset.saving; }, { once: true });
}

// ---------- Usage ----------
views.usage = async function (root) {
  const dim = root._d || 'model', hours = root._h || 24, filter = root._f || 'all';
  const [rows0, series0, reqs0] = await Promise.all([api(`usage?group=${dim}&hours=${hours}`), api(`usage?group=hour&hours=${hours}`), api('requests?limit=200')]);
  const rows = rows0 || [], series = series0 || [], reqs = reqs0 || [];
  const t = series.reduce((a, r) => ({ req: a.req + r.requests, err: a.err + r.errors, cost: a.cost + r.cost_idr, tin: a.tin + r.in_tok, tout: a.tout + r.out_tok, lat: a.lat + r.avg_latency_ms * r.requests }), { req: 0, err: 0, cost: 0, tin: 0, tout: 0, lat: 0 });
  const stat = (label, value, sub) => h('div', { class: 'col-6 col-lg-3' }, h('div', { class: 'card' }, h('div', { class: 'card-body' }, h('div', { class: 'subheader' }, label), h('div', { class: 'h2 mb-0' }, value), sub && h('div', { class: 'text-secondary small' }, sub))));
  const seg = (opts, cur, on) => h('div', { class: 'btn-group' }, opts.map(([v, l]) => h('button', { class: 'btn btn-sm ' + (String(cur) === String(v) ? 'btn-primary' : 'btn-outline-primary'), onclick: () => on(v) }, l)));
  const max = Math.max(1, ...rows.map(r => r.requests));
  const shown = reqs.filter(r => filter === 'all' || !r.ok);
  root.replaceChildren(
    pageHeader('Usage', null, seg([[1, '1 h'], [24, '24 h'], [168, '7 d'], [720, '30 d']], hours, v => { root._h = +v; render(); })),
    h('div', { class: 'row row-cards mb-3' }, stat('Requests', num(t.req), `${num(t.tin)} in · ${num(t.tout)} out tokens`), stat('Errors', t.req ? (t.err / t.req * 100).toFixed(1) + '%' : '0%', `${num(t.err)} failed`),
      stat('Average time', t.req ? Math.round(t.lat / t.req) + ' ms' : '–', 'per request'), stat('Cost at list price', 'Rp ' + idr(t.cost), 'free and plan traffic included')),
    h('div', { class: 'mb-3' }, card('Requests over time', chart(series, hours))),
    h('div', { class: 'mb-3' }, card('Breakdown', h('div', {}, h('div', { class: 'mb-2' }, seg([['model', 'Model'], ['preset', 'Preset'], ['client', 'Client'], ['upstream', 'Key']], dim, v => { root._d = v; render(); })),
      rows.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-vcenter' }, h('thead', {}, h('tr', {}, [dim === 'upstream' ? 'key' : dim, 'requests', '', 'errors', 'tokens in / out', 'avg ms', 'Rp (list)'].map(x => h('th', {}, x)))),
        h('tbody', {}, rows.filter(r => r.key).map(r => h('tr', {}, h('td', { class: 'mono' }, r.key), h('td', {}, num(r.requests)), h('td', { style: 'min-width:90px' }, progress(r.requests, max)), h('td', { class: r.errors ? 'text-danger' : 'text-secondary' }, r.errors), h('td', {}, `${num(r.in_tok)} / ${num(r.out_tok)}`), h('td', {}, Math.round(r.avg_latency_ms)), h('td', {}, idr(r.cost_idr))))))) : empty('No traffic in this period.')))),
    card('Recent requests', h('div', {}, h('div', { class: 'mb-2' }, seg([['all', 'All'], ['err', 'Errors only']], filter, v => { root._f = v; render(); })),
      shown.length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm table-vcenter' }, h('thead', {}, h('tr', {}, ['When', 'Client', 'Asked for', 'Served by', 'Key', 'Result', 'Time', 'Tokens in/out', 'Notes'].map(x => h('th', {}, x)))),
        h('tbody', {}, shown.slice(0, 100).map(r => h('tr', {}, h('td', { class: 'nowrap text-secondary' }, ago(r.ts) + ' ago'), h('td', {}, r.client), h('td', { class: 'mono' }, r.requested || r.preset), h('td', { class: 'mono' }, r.model), h('td', {}, r.upstream),
          h('td', {}, badge(r.ok ? 'ok' : (r.status || 'error'), r.ok ? 'green' : 'red')), h('td', { class: 'nowrap' }, r.latency_ms + ' ms'), h('td', { class: 'nowrap' }, `${r.in_tok} / ${r.out_tok}`),
          h('td', {}, r.attempt > 1 && badge('retry ' + r.attempt, 'orange'), r.canary && badge('trial', 'azure'), r.rtk_saved > 0 && badge('saved ' + r.rtk_saved, 'green'), r.err && h('span', { class: 'text-danger small' }, r.err))))))) : empty('Nothing to show.'))));
};

function chart(series, hours) {
  const byDay = hours > 72, map = new Map();
  for (const r of series) { const k = byDay ? r.key.slice(0, 10) : r.key.slice(11, 16); const o = map.get(k) || { req: 0, err: 0 }; o.req += r.requests; o.err += r.errors; map.set(k, o); }
  const pts = [...map.entries()];
  if (!pts.length) return empty('No traffic in this period.');
  const W = 600, H = 100, bw = W / pts.length, max = Math.max(1, ...pts.map(([, v]) => v.req));
  const NS = 'http://www.w3.org/2000/svg', svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${W} ${H + 14}`); svg.setAttribute('class', 'chart'); svg.setAttribute('preserveAspectRatio', 'none');
  pts.forEach(([k, v], i) => {
    const hh = v.req / max * H, r = document.createElementNS(NS, 'rect');
    r.setAttribute('x', i * bw + 1); r.setAttribute('width', Math.max(1, bw - 2)); r.setAttribute('y', H - hh); r.setAttribute('height', hh); if (v.err && v.err / v.req > .2) r.setAttribute('class', 'err');
    const t = document.createElementNS(NS, 'title'); t.textContent = `${k}: ${v.req} requests, ${v.err} errors`; r.append(t); svg.append(r);
  });
  [0, pts.length - 1].forEach(i => { const tx = document.createElementNS(NS, 'text'); tx.setAttribute('x', i === 0 ? 0 : W); tx.setAttribute('y', H + 12); tx.setAttribute('text-anchor', i === 0 ? 'start' : 'end'); tx.textContent = pts[i][0]; svg.append(tx); });
  return svg;
}

// ---------- Settings ----------
views.settings = async function (root) {
  await ensureCfg();
  const c = S.cfg, j = await api('jev'), R = c.rules, cat = await api('catalog?modality=chat');
  const bench = Object.entries(c.benchmarks);
  const L = { free_bonus: 'Preference for free models', plan_bonus: 'Preference for plan-covered models', plan_cost_factor: 'Cost of plan-covered models (0-1)', promo_bonus_max: 'Preference for discounted models',
    promo_min_remaining_hours: 'Ignore discounts ending within (h)', upcoming_lookahead_hours: 'Look ahead for price changes (h)', new_model_days: 'A model is "new" for (days)', canary_min_requests: 'Requests to trust a new model', canary_min_success: 'Success rate to trust it (0-1)', stats_window_hours: 'Health window (h)' };
  const RL = { max_attempts: 'Models to try per request', cooldown_base_s: 'First pause after an error (s)', cooldown_max_s: 'Longest pause (s)', breaker_failures: 'Errors in a row before a key is paused', breaker_open_s: 'How long a paused key rests (s)' };
  root.replaceChildren(pageHeader('Settings', 'Sensible defaults are already set. Most of this you never need to touch.'),
    h('div', { class: 'row row-cards' },
      h('div', { class: 'col-lg-6' }, card('Smart routing', h('div', {},
        field(R, 'canary_percent', { type: 'num', label: 'Try new models on this share of traffic (%)', help: 'new models are tried in small doses before they are trusted' }),
        field(R, 'sticky_minutes', { type: 'num', label: 'Keep one conversation on one model (minutes)', help: 'keeps the provider cache warm and answers consistent' }),
        field(R, 'avoid_sunset_days', { type: 'num', label: 'Stop using models that end within (days)' }),
        advanced(cols(['free_bonus', 'plan_bonus', 'plan_cost_factor', 'promo_bonus_max'].map(k => field(R, k, { type: 'num', step: '0.01', label: L[k] })), ['promo_min_remaining_hours', 'upcoming_lookahead_hours', 'new_model_days', 'canary_min_requests', 'canary_min_success', 'stats_window_hours'].map(k => field(R, k, { type: 'num', label: L[k] }))))), { class: 'h-100' })),
      h('div', { class: 'col-lg-6' }, card('Token saver (RTK)', h('div', {}, h('p', { class: 'text-secondary' }, 'Shrinks big tool output (git diff, grep, ls, logs) before it is sent and paid for. Your own messages are never touched.'),
        field(c.rtk, 'enabled', { type: 'switch', label: 'Token saver on' }),
        h('div', { class: 'row' }, [['git_diff', 'git diff'], ['grep', 'grep results'], ['ls', 'directory listings'], ['logs', 'logs'], ['truncate', 'any very long output']].map(([k, l]) => h('div', { class: 'col-6' }, field(c.rtk, k, { type: 'switch', label: l })))),
        advanced(cols(field(c.rtk, 'min_chars', { type: 'num', label: 'Only outputs longer than (characters)' }), field(c.rtk, 'max_chars', { type: 'num', label: 'Hard cap (characters)' }), field(c.rtk, 'diff_context', { type: 'num', label: 'Context lines kept around diff changes' })))), { class: 'h-100' })),
      h('div', { class: 'col-lg-6' }, card('Quality judge (JEV)', h('div', {}, h('p', { class: 'text-secondary' }, 'Candidate models answer a few test questions and kenari\'s System One model scores them. Scores decide which models are good enough. It costs a little, so a daily limit applies.'),
        h('div', { class: 'mb-2' }, badge(j.enabled ? 'on' : 'off', j.enabled ? 'green' : 'secondary'), badge(`today Rp ${idr(j.spent_idr)} of Rp ${idr(j.budget_idr)}`)),
        field(c.jev, 'enabled', { type: 'switch', label: 'Judge models automatically' }), field(c.jev, 'daily_budget_idr', { type: 'num', label: 'Daily limit (Rp)' }),
        h('button', { class: 'btn btn-outline-primary btn-sm mb-3', onclick: () => api('jev/run', { method: 'POST', body: {} }).then(() => toast('Started in the background; check back in a minute'), fail) }, 'Judge pending models now'),
        Object.keys(j.scores || {}).length ? h('div', { class: 'table-responsive' }, h('table', { class: 'table table-sm' }, h('thead', {}, h('tr', {}, ['Model', 'Score', 'Judged'].map(x => h('th', {}, x)))), h('tbody', {}, Object.entries(j.scores).sort((a, b) => b[1].Score - a[1].Score).map(([m, s]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', {}, s.Score.toFixed(2)), h('td', { class: 'text-secondary' }, ago(s.TS) + ' ago')))))) : h('div', { class: 'text-secondary small' }, 'No scores yet.'),
        advanced(cols(field(c.jev, 'model', { label: 'Judge model' }), field(c.jev, 'pass_threshold', { type: 'num', step: '0.01', label: 'Minimum score to be used (0-1)' }), field(c.jev, 'every_hours', { type: 'num', label: 'Re-judge every (hours)' }), field(c.jev, 'upstream', { label: 'Use only this key' })), field(c.jev, 'prompts', { type: 'lines', label: 'Test questions (one per line)' }))), { class: 'h-100' })),
      h('div', { class: 'col-lg-6' }, card('Known quality scores', h('div', {}, h('p', { class: 'text-secondary' }, 'Without a score from you, quality is estimated from price. If you trust a benchmark for a model, enter it here (0-100).'),
        h('datalist', { id: 'models' }, cat.map(m => h('option', { value: m.id }))),
        bench.length ? h('table', { class: 'table table-sm' }, h('tbody', {}, bench.map(([m, v]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', { style: 'width:110px' }, h('input', { class: 'form-control form-control-sm', type: 'number', min: 0, max: 100, value: v, onchange: e => { c.benchmarks[m] = Number(e.target.value); markDirty(); } })), h('td', { class: 'text-end' }, h('button', { class: 'btn btn-sm btn-ghost-danger', onclick: () => { delete c.benchmarks[m]; markDirty(); render(); } }, 'Remove')))))) : h('div', { class: 'text-secondary small mb-2' }, 'None set: using the price-based estimate.'),
        h('button', { class: 'btn btn-outline-primary btn-sm', onclick: () => { const m = prompt('Model id (e.g. deepseek-v4-1-flash):'); if (m) { c.benchmarks[m.trim()] = 70; markDirty(); render(); } } }, '+ Add a score')), { class: 'h-100' })),
      h('div', { class: 'col-12' }, card('Reliability and access', h('div', {}, advanced(cols(
        ['max_attempts', 'cooldown_base_s', 'cooldown_max_s', 'breaker_failures', 'breaker_open_s'].map(k => field(c.resilience, k, { type: 'num', label: RL[k] })),
        field(c.server, 'admin_hosts', { type: 'list', label: 'Addresses allowed to open this panel' }), field(c.server, 'session_hours', { type: 'num', label: 'Stay signed in for (hours)' }), field(c.server, 'log_retention_days', { type: 'num', label: 'Keep the request log (days)' }), field(c.kenari, 'sync_minutes', { type: 'num', label: 'Refresh prices every (minutes)' }))),
        h('button', { class: 'btn btn-outline-secondary btn-sm mt-3', onclick: () => api('logout-all', { method: 'POST' }).then(() => location.reload(), fail) }, 'Sign out everywhere'))))));
};

// ---------- Config file ----------
views.config = async function (root) {
  await loadCfg();
  const ta = h('textarea', { class: 'form-control mono', rows: 22, spellcheck: 'false' }); ta.value = S.yaml;
  root.replaceChildren(pageHeader('Config file', 'The same settings as plain text. Saving checks everything first; the previous version is kept and can be restored.'),
    h('div', { class: 'mb-3' }, card(null, h('div', {}, ta, h('div', { class: 'd-flex gap-2 mt-3' }, h('button', { class: 'btn btn-primary', onclick: async () => { try { await api('config', { method: 'PUT', body: { yaml: ta.value } }); toast('Saved and applied'); render(); } catch (e) { fail(e); } } }, 'Check, save & apply'), h('button', { class: 'btn btn-ghost-secondary', onclick: () => render() }, 'Reload'))))),
    card('Earlier versions', S.history.length ? S.history.slice(0, 20).map(id => h('div', { class: 'd-flex justify-content-between align-items-center py-1' }, h('span', { class: 'mono' }, id), h('button', { class: 'btn btn-sm btn-outline-secondary', onclick: async () => { if (!confirm('Restore ' + id + '?')) return; try { await api('rollback', { method: 'POST', body: { id } }); toast('Restored'); render(); } catch (e) { fail(e); } } }, 'Restore'))) : empty('No earlier versions yet.')));
};

// ---------- shell ----------
function go(tab) {
  if (S.dirty && !confirm('Discard unsaved changes?')) return;
  S.dirty = false; $('#savebar').hidden = true; S.cfg = null; S.tab = tab; render();
}

async function render() {
  const root = $('#view');
  [...$('#nav').children].forEach(li => li.firstChild.classList.toggle('active', li.firstChild.dataset.tab === S.tab));
  clearInterval(S.timer);
  try {
    const fresh = h('div');
    fresh._d = root._d; fresh._h = root._h; fresh._f = root._f;
    await views[S.tab](fresh);
    root._d = fresh._d; root._h = fresh._h; root._f = fresh._f;
    root.replaceChildren(...fresh.childNodes);
    if (fresh._refresh) S.timer = setInterval(() => { if (!document.hidden && !$('#dlg').open) render(); }, fresh._refresh);
  } catch (e) { if (e.message !== 'login required') root.replaceChildren(h('div', { class: 'alert alert-danger' }, e.message)); }
}

function showLogin() { $('#app').hidden = true; $('#login').hidden = false; }
function showApp() { $('#login').hidden = true; $('#app').hidden = false; }

$('#nav').append(...TABS.map(([id, label]) => h('li', { class: 'nav-item' }, h('a', { class: 'nav-link clickable', 'data-tab': id, onclick: () => go(id) }, label))));
$('#save').onclick = saveCfg;
$('#discard').onclick = async () => { await loadCfg(); render(); };
$('#logout').onclick = () => api('logout', { method: 'POST' }).then(showLogin, fail);
$('#theme').onclick = () => { const t = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark'; document.documentElement.setAttribute('data-bs-theme', t); try { localStorage.setItem('xl-theme', t); } catch (e) { /* ignore */ } };
$('#loginform').addEventListener('submit', async e => {
  e.preventDefault(); const f = new FormData(e.target); $('#loginerr').textContent = '';
  try { await api('login', { method: 'POST', body: { username: f.get('username'), password: f.get('password'), code: f.get('code') } }); e.target.reset(); showApp(); render(); }
  catch (err) { $('#loginerr').textContent = err.message; }
});
window.addEventListener('beforeunload', e => { if (S.dirty) { e.preventDefault(); e.returnValue = ''; } });
api('me').then(() => { showApp(); render(); }, () => showLogin());
