/* xellter admin panel: dependency-free, talks to /admin/api/*. */
'use strict';

const $ = (s, e = document) => e.querySelector(s);
const S = { cfg: null, yaml: '', history: [], tab: 'overview', dirty: false, timer: null };
const TABS = [['overview', 'Overview'], ['shadows', 'Shadow models'], ['upstreams', 'Upstreams'], ['clients', 'Clients'], ['policy', 'Policy'], ['usage', 'Usage'], ['config', 'Config']];

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'value') el.value = v;
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
  const t = $('#toast'); t.textContent = msg; t.style.background = bad ? 'var(--bad)' : ''; t.style.color = bad ? '#fff' : '';
  t.classList.add('show'); clearTimeout(t._t); t._t = setTimeout(() => t.classList.remove('show'), bad ? 6000 : 2500);
}
const fail = e => toast(e.message || String(e), true);

const idr = n => n == null ? '–' : n === 0 ? '0' : (n >= 100 ? Math.round(n).toLocaleString('en-US') : n >= 1 ? n.toFixed(2) : n.toFixed(4));
const ago = ts => { const s = Math.max(0, Math.round((Date.now() - ts) / 1000)); return s < 90 ? s + 's' : s < 5400 ? Math.round(s / 60) + 'm' : s < 172800 ? Math.round(s / 3600) + 'h' : Math.round(s / 86400) + 'd'; };
const until = t => { const ms = new Date(t) - Date.now(); return ms > 0 ? Math.ceil(ms / 1000) + 's' : ''; };
const chip = (t, c) => h('span', { class: 'chip ' + (c || '') }, t);
const pct = (a, b) => b > 0 ? Math.min(100, a / b * 100) : 0;
const bar = (a, b) => { const p = pct(a, b); return h('div', { class: 'bar' + (p > 95 ? ' full' : p > 75 ? ' hot' : '') }, h('i', { style: `width:${p}%` })); };

// ---------- form helpers (mutate S.cfg in place and mark dirty) ----------
function markDirty() { S.dirty = true; $('#savebar').hidden = false; }
function field(obj, key, o = {}) {
  const type = o.type || 'text', label = o.label || key;
  let input;
  const set = v => { obj[key] = v; markDirty(); };
  if (type === 'bool') {
    input = h('input', { type: 'checkbox', onchange: e => set(e.target.checked) }); input.checked = !!obj[key];
    return h('label', { class: 'f check' }, input, h('span', {}, label), o.help && h('span', { class: 'help' }, o.help));
  }
  if (type === 'optbool') {
    input = h('select', { onchange: e => { obj[key] = e.target.value === '' ? null : e.target.value === 'true'; markDirty(); } },
      h('option', { value: '' }, 'inherit global'), h('option', { value: 'true' }, 'on'), h('option', { value: 'false' }, 'off'));
    input.value = obj[key] == null ? '' : String(obj[key]);
  } else if (type === 'select') {
    input = h('select', { onchange: e => set(e.target.value) }, o.opts.map(x => h('option', { value: x }, x))); input.value = obj[key] ?? o.opts[0];
  } else if (type === 'num') {
    input = h('input', { type: 'number', step: o.step || 'any', onchange: e => set(e.target.value === '' ? 0 : Number(e.target.value)), value: obj[key] ?? 0 });
  } else if (type === 'list') {
    input = h('input', { placeholder: o.ph || 'comma separated', onchange: e => set(e.target.value.split(',').map(s => s.trim()).filter(Boolean)), value: (obj[key] || []).join(', ') });
  } else if (type === 'lines') {
    input = h('textarea', { onchange: e => set(e.target.value.split('\n').map(s => s.trim()).filter(Boolean)) }); input.value = (obj[key] || []).join('\n');
  } else {
    input = h('input', { onchange: e => set(e.target.value), value: obj[key] ?? '', readonly: o.readonly, placeholder: o.ph });
  }
  return h('label', { class: 'f' }, h('span', {}, label), input, o.help && h('span', { class: 'help' }, o.help));
}

async function loadCfg() {
  const j = await api('config');
  S.cfg = j.config; S.yaml = j.yaml; S.history = j.history || []; S.dirty = false; $('#savebar').hidden = true;
  S.cfg.upstreams ||= []; S.cfg.clients ||= []; S.cfg.shadows ||= {}; S.cfg.benchmarks ||= {};
}

async function saveCfg() {
  try { await api('config', { method: 'PUT', body: { config: S.cfg } }); await loadCfg(); toast('Saved and applied'); render(); }
  catch (e) { fail(e); }
}

function showDialog(...kids) { const d = $('#dlgbody'); d.replaceChildren(...kids.flat(Infinity).filter(Boolean)); const dlg = $('#dlg'); if (!dlg.open) dlg.showModal(); }

// ---------- views ----------
const views = {};

views.overview = async function (root) {
  const o = await api('overview');
  const c = o.catalog || {};
  root.replaceChildren(
    h('div', { class: 'grid' },
      h('div', { class: 'card' }, h('div', { class: 'muted' }, 'Requests since start'), h('div', { class: 'stat' }, o.requests.total, h('small', {}, ` · ${o.requests.failed} failed · ${o.requests.inflight} in flight`))),
      h('div', { class: 'card' }, h('div', { class: 'muted' }, 'Catalog'), h('div', { class: 'stat' }, c.models ?? 0, h('small', {}, ` models · ${c.plans || 0} plans · ${c.age_s ?? '?'}s old`)),
        c.degraded && chip('stale (kenari unreachable)', 'warn'),
        h('button', { class: 'ghost', onclick: () => api('catalog/refresh', { method: 'POST' }).then(() => { toast('Catalog refreshed'); render(); }, fail) }, 'Refresh now')),
      h('div', { class: 'card' }, h('div', { class: 'muted' }, 'RTK saved'), h('div', { class: 'stat' }, Math.round(o.requests.rtk_saved_chars / 1000), h('small', {}, ' k chars removed'))),
      h('div', { class: 'card' }, h('div', { class: 'muted' }, 'JEV budget today'), h('div', { class: 'stat' }, idr(o.jev.spent_idr), h('small', {}, ` / Rp${idr(o.jev.budget_idr)}`)), chip(o.jev.enabled ? 'enabled' : 'disabled', o.jev.enabled ? 'ok' : ''))),
    h('h2', {}, 'Upstream keys'),
    h('div', { class: 'grid wide' }, (o.keys || []).map(k => {
      const cd = until(k.cooldown_until), br = until(k.breaker_until), pb = until(k.plan_blocked_until);
      return h('div', { class: 'card' },
        h('div', { class: 'row spread' }, h('strong', {}, k.name), h('span', {}, chip(k.tier + (k.plan ? ' · ' + k.plan : '')), !k.enabled && chip('disabled', 'bad'), !k.has_secret && chip('no key value', 'bad'))),
        h('div', { class: 'help' }, `rpm ${k.rpm_used}/${k.rpm_limit || '∞'}`), bar(k.rpm_used, k.rpm_limit),
        k.daily_limit > 0 && [h('div', { class: 'help' }, `free requests (24h) ${k.daily_used}/${k.daily_limit}`), bar(k.daily_used, k.daily_limit)],
        k.plan && ['5h', 'week', 'month'].map((w, i) => k.plan_budget[i] > 0 && [h('div', { class: 'help' }, `plan ${w}: Rp${idr(k.plan_spent[i])} / Rp${idr(k.plan_budget[i])}`), bar(k.plan_spent[i], k.plan_budget[i])]),
        h('div', {}, cd && chip('cooldown ' + cd, 'warn'), br && chip('breaker open ' + br, 'bad'), pb && chip('plan limit ' + pb, 'warn'),
          (k.blocked_models || []).map(m => chip('blocked ' + m, 'warn')), k.learned_allow?.length ? chip(`restricted to ${k.learned_allow.length} models`) : null),
        k.last_error && h('div', { class: 'help bad' }, k.last_error),
        h('div', { class: 'row' },
          h('button', { class: 'ghost', onclick: () => api('keys/reset', { method: 'POST', body: { name: k.name } }).then(() => { toast('State cleared'); render(); }, fail) }, 'Clear cooldowns'),
          h('button', { class: 'ghost', onclick: () => api('keys/probe', { method: 'POST', body: {} }).then(() => { toast('Re-checked which models each key may use'); render(); }, fail) }, 'Re-probe models')));
    })),
    h('h2', {}, 'Last 24 hours'),
    h('div', { class: 'grid wide' },
      usageCard('By client', o.by_client), usageCard('By shadow model', o.by_shadow)),
    h('h2', {}, 'Clients today'),
    h('div', { class: 'card scroll' }, h('table', {}, h('thead', {}, h('tr', {}, ['Client', 'Spent today (Rp)', 'Budget', 'State'].map(x => h('th', {}, x)))),
      h('tbody', {}, (o.clients || []).map(cl => h('tr', {}, h('td', {}, cl.name), h('td', { class: 'num' }, idr(cl.spent_today)), h('td', {}, cl.budget > 0 ? [`Rp${idr(cl.budget)}`, bar(cl.spent_today, cl.budget)] : 'unlimited'), h('td', {}, chip(cl.enabled ? 'enabled' : 'disabled', cl.enabled ? 'ok' : 'bad'))))))));
};

function usageCard(title, rows) {
  return h('div', { class: 'card scroll' }, h('h3', {}, title), h('table', {}, h('thead', {}, h('tr', {}, ['', 'req', 'err', 'in tok', 'out tok', 'Rp'].map((x, i) => h('th', { class: i ? 'num' : '' }, x)))),
    h('tbody', {}, (rows || []).filter(r => r.key).map(r => h('tr', {}, h('td', {}, r.key), h('td', { class: 'num' }, r.requests), h('td', { class: 'num' }, r.errors), h('td', { class: 'num' }, r.in_tok), h('td', { class: 'num' }, r.out_tok), h('td', { class: 'num' }, idr(r.cost_idr)))))));
}

views.shadows = async function (root) {
  await ensureCfg();
  const rk = await api('ranking');
  const kids = [h('div', { class: 'row spread' }, h('h2', {}, 'Shadow models'),
    h('button', { onclick: addShadow }, '+ New shadow'))];
  kids.push(h('p', { class: 'muted' }, 'Clients call these names; xellter picks the real model. "auto" ranks the catalog by cost, benchmark, JEV score, reliability and context; "combo" is an explicit ordered list.'));
  for (const name of Object.keys(S.cfg.shadows).sort()) {
    const sh = S.cfg.shadows[name], r = rk[name];
    sh.require ||= {}; sh.weights ||= {}; sh.cost_profile ||= {};
    const top = r && r.candidates && r.candidates[0];
    kids.push(h('details', { class: 'card' },
      h('summary', {}, h('strong', {}, name), chip(sh.modality), chip(sh.mode), top ? chip('now: ' + top.model, 'ok') : chip('no candidate', 'bad'), r && r.trial?.length ? chip(r.trial.length + ' on trial', 'warn') : null),
      h('div', { class: 'grid' },
        field(sh, 'description', { label: 'Description' }),
        field(sh, 'modality', { type: 'select', opts: ['chat', 'images', 'videos', 'audio_speech', 'audio_transcription', 'music'] }),
        field(sh, 'mode', { type: 'select', opts: ['auto', 'combo'] }),
        field(sh, 'strategy', { type: 'select', opts: ['round_robin', 'priority'], help: 'round_robin rotates among models whose scores are within rr_band' }),
        field(sh, 'max_candidates', { type: 'num', label: 'Max candidates' }),
        field(sh, 'min_quality', { type: 'num', step: '0.01', label: 'Min quality (0-1)', help: 'weighted benchmark, JEV and context score a model needs' }),
        field(sh, 'rr_band', { type: 'num', step: '0.01', label: 'Round-robin band' })),
      h('div', { class: 'sect' }, 'Combo list / filters'),
      h('div', { class: 'grid wide' },
        field(sh, 'models', { type: 'list', label: 'Combo models (mode=combo, in order)' }),
        field(sh, 'allow', { type: 'list', label: 'Allow globs (auto)', ph: 'empty = whole catalog, e.g. deepseek-*, glm-*' }),
        field(sh, 'deny', { type: 'list', label: 'Deny globs' })),
      h('div', { class: 'sect' }, 'Hard requirements'),
      h('div', { class: 'grid' }, field(sh.require, 'tool_call', { type: 'bool', label: 'Tool calling' }), field(sh.require, 'vision', { type: 'bool', label: 'Vision input' }), field(sh.require, 'reasoning', { type: 'bool', label: 'Reasoning' }), field(sh.require, 'min_context', { type: 'num', label: 'Min context tokens' })),
      h('div', { class: 'sect' }, 'Score weights (relative)'),
      h('div', { class: 'grid' }, ['cost', 'benchmark', 'jev', 'reliability', 'context'].map(k => field(sh.weights, k, { type: 'num' }))),
      h('div', { class: 'sect' }, 'Reference request for pricing'),
      h('div', { class: 'grid' }, [['input_tokens', 'num'], ['output_tokens', 'num'], ['cached_ratio', 'num'], ['images', 'num'], ['seconds', 'num'], ['chars', 'num'], ['variant', 'text']].map(([k, t]) => field(sh.cost_profile, k, { type: t }))),
      h('div', { class: 'row', style: 'margin-top:12px' },
        h('button', { onclick: () => showRanking(name) }, 'Why this order?'),
        h('button', { class: 'danger', onclick: () => { if (confirm('Delete shadow ' + name + '?')) { delete S.cfg.shadows[name]; markDirty(); render(); } } }, 'Delete'))));
  }
  root.replaceChildren(...kids);
};

function addShadow() {
  const name = (prompt('Name of the new shadow model (for example xl-research):') || '').trim();
  if (!name) return;
  if (S.cfg.shadows[name]) return toast('That name exists', true);
  S.cfg.shadows[name] = JSON.parse(JSON.stringify(S.cfg.shadows['xl-chat'] || Object.values(S.cfg.shadows)[0]));
  S.cfg.shadows[name].description = ''; markDirty(); render();
}

async function showRanking(name) {
  try {
    const r = await api('ranking?shadow=' + encodeURIComponent(name));
    const row = (c, i) => h('tr', {}, h('td', { class: 'num' }, i + 1), h('td', { class: 'mono' }, c.model), h('td', { class: 'num' }, c.score.toFixed(3)), h('td', { class: 'num' }, c.quality.toFixed(2)),
      h('td', { class: 'num' }, c.cost_idr ? idr(c.cost_idr) : 'free/plan'), h('td', {}, (c.flags || []).map(f => chip(f, /free|plan|promo|proven/.test(f) ? 'ok' : 'warn'))),
      h('td', { class: 'help' }, Object.entries(c.components || {}).map(([k, v]) => `${k} ${v}`).join(' · '), c.bench_source !== 'configured' ? ` · bench:${c.bench_source}` : ''));
    const tbl = (list) => h('div', { class: 'scroll' }, h('table', {}, h('thead', {}, h('tr', {}, ['#', 'model', 'score', 'quality', 'Rp/ref req', 'flags', 'components'].map(x => h('th', {}, x)))), h('tbody', {}, list.map(row))));
    showDialog(h('h2', {}, `${name} · ranking`), h('p', { class: 'muted' }, `Round-robin group: first ${r.front} candidate(s). Computed ${ago(new Date(r.computed))} ago.`),
      r.candidates?.length ? tbl(r.candidates) : h('p', { class: 'bad' }, 'No eligible candidates.'),
      r.trial?.length ? [h('h3', {}, 'On trial (receive canary traffic)'), tbl(r.trial)] : null,
      r.unavailable?.length ? h('p', { class: 'muted' }, `${r.unavailable.length} catalog models of this modality are not callable with the configured keys: ` + r.unavailable.slice(0, 12).join(', ') + (r.unavailable.length > 12 ? ', …' : '')) : null,
      r.rejected?.length ? [h('h3', {}, `Rejected (${r.rejected.length})`), h('div', { class: 'scroll' }, h('table', {}, h('tbody', {}, r.rejected.map(x => h('tr', {}, h('td', { class: 'mono' }, x.model), h('td', { class: 'muted' }, x.why))))))] : null);
  } catch (e) { fail(e); }
}

views.upstreams = async function (root) {
  await ensureCfg();
  const o = await api('overview');
  const st = Object.fromEntries((o.keys || []).map(k => [k.name, k]));
  const kids = [h('div', { class: 'row spread' }, h('h2', {}, 'Upstream keys'), h('button', { onclick: () => { S.cfg.upstreams.push({ name: 'new-key', key_env: 'KENARI_KEY_NEW', enabled: true, tier: 'free', plan: '', rpm: 0, daily_free: 0, allow: [], note: '' }); markDirty(); render(); } }, '+ Add key')),
    h('p', { class: 'muted' }, 'Key values are never stored in the config: they live in the secrets file (mode 0600). Tier decides rate limits: free 5 rpm / 50 free requests a day, topup 10 rpm / 1000, plan 25 rpm.')];
  S.cfg.upstreams.forEach((u, i) => {
    const s = st[u.name];
    kids.push(h('div', { class: 'card' },
      h('div', { class: 'row spread' }, h('strong', {}, u.name), s && (s.has_secret ? chip('key value set', 'ok') : chip('key value missing', 'bad'))),
      h('div', { class: 'grid' },
        field(u, 'name'), field(u, 'key_env', { label: 'Secret name', help: 'environment-style name, e.g. KENARI_KEY_BABU' }), field(u, 'account', { label: 'Account', help: 'keys of one kenari account share plan budget; empty = own' }),
        field(u, 'tier', { type: 'select', opts: ['free', 'topup', 'plan'] }), field(u, 'plan', { label: 'Plan name', help: 'for tier=plan, e.g. Indie' }),
        field(u, 'rpm', { type: 'num', label: 'RPM override', help: '0 = tier default' }), field(u, 'daily_free', { type: 'num', label: 'Daily free override', help: '0 = tier default, -1 = unlimited' })),
      h('div', { class: 'grid wide' }, field(u, 'allow', { type: 'list', label: 'Allowed models (globs)', ph: 'empty = all; restricted keys are learned automatically' }), field(u, 'note')),
      field(u, 'enabled', { type: 'bool' }),
      h('div', { class: 'row', style: 'margin-top:8px' },
        h('button', { onclick: async () => { const v = prompt(`Paste the API key value for "${u.key_env}" (saved to the secrets file):`); if (v) try { await api('secret', { method: 'PUT', body: { name: u.key_env, value: v } }); toast('Key value stored'); render(); } catch (e) { fail(e); } } }, 'Set key value'),
        h('button', { class: 'danger', onclick: () => { if (confirm('Remove upstream ' + u.name + '?')) { S.cfg.upstreams.splice(i, 1); markDirty(); render(); } } }, 'Remove')),
      h('p', { class: 'help' }, 'Set key value only works for secret names already saved in the config: save first after adding a key.')));
  });
  root.replaceChildren(...kids);
};

views.clients = async function (root) {
  await ensureCfg();
  const names = S.cfg.upstreams.map(u => u.name).join(', ');
  const kids = [h('div', { class: 'row spread' }, h('h2', {}, 'Clients'), h('button', { class: 'primary', onclick: newClient }, '+ New client')),
    h('p', { class: 'muted' }, 'One API key per client; a key cannot be used by another client. Keys are stored only as hashes and shown once.')];
  S.cfg.clients.forEach((c, i) => {
    kids.push(h('div', { class: 'card' },
      h('div', { class: 'row spread' }, h('strong', {}, c.name), h('span', { class: 'mono muted' }, 'key ' + (c.key_hint || '–'))),
      h('div', { class: 'grid' }, field(c, 'daily_budget_idr', { type: 'num', label: 'Daily budget (Rp, 0 = none)' }), field(c, 'rtk', { type: 'optbool', label: 'RTK compression' })),
      h('div', { class: 'grid wide' },
        field(c, 'shadows', { type: 'list', label: 'Allowed shadow models', ph: '* or xl-chat, xl-image' }),
        field(c, 'passthrough', { type: 'list', label: 'Direct model globs (real models)', ph: 'empty = none; e.g. deepseek-v4-1-flash' }),
        field(c, 'upstreams', { type: 'list', label: 'Restrict to upstreams', ph: 'empty = all (' + names + ')' }),
        field(c, 'note')),
      field(c, 'enabled', { type: 'bool' }),
      h('div', { class: 'row', style: 'margin-top:8px' },
        h('button', { onclick: async () => { if (!confirm('Rotate the key of ' + c.name + '? The old key stops working immediately.')) return; if (S.dirty) return toast('Save or discard pending changes first', true); try { const r = await api('clients', { method: 'POST', body: { action: 'rotate', name: c.name } }); await loadCfg(); showKey(c.name, r.key); render(); } catch (e) { fail(e); } } }, 'Rotate key'),
        h('button', { class: 'danger', onclick: () => { if (confirm('Delete client ' + c.name + '?')) { S.cfg.clients.splice(i, 1); markDirty(); render(); } } }, 'Delete'))));
  });
  root.replaceChildren(...kids);
};

function showKey(name, key) {
  showDialog(h('h2', {}, `API key for ${name}`), h('p', { class: 'muted' }, 'Copy it now: it is not stored and cannot be shown again. Use it as the OpenAI-style bearer token (or x-api-key) with base URL ' + location.origin + '/v1'),
    h('div', { class: 'keybox mono' }, key));
}

function newClient() {
  if (S.dirty) return toast('Save or discard pending changes first', true);
  const c = { name: '', shadows: ['*'], passthrough: [], upstreams: [], daily_budget_idr: 0, note: '' };
  showDialog(h('h2', {}, 'New client'), field(c, 'name', { ph: 'e.g. babu' }), field(c, 'shadows', { type: 'list', label: 'Allowed shadow models' }), field(c, 'passthrough', { type: 'list', label: 'Direct model globs' }), field(c, 'daily_budget_idr', { type: 'num', label: 'Daily budget (Rp)' }),
    h('button', { class: 'primary', onclick: async () => { try { const r = await api('clients', { method: 'POST', body: { action: 'create', client: c } }); await loadCfg(); S.dirty = false; $('#savebar').hidden = true; showKey(c.name, r.key); render(); } catch (e) { fail(e); } } }, 'Create'));
}

views.policy = async function (root) {
  await ensureCfg();
  const c = S.cfg, j = await api('jev');
  const R = c.rules;
  const benchRows = Object.entries(c.benchmarks);
  const cat = await api('catalog?modality=chat');
  root.replaceChildren(
    h('h2', {}, 'Auto-route policy'),
    h('div', { class: 'card' }, h('h3', {}, 'Rules'), h('p', { class: 'muted' }, 'Bonuses are added to a model\'s 0-1 score. Promos and free tiers are read live from kenari.'),
      h('div', { class: 'grid' }, [['free_bonus', 'Free model bonus'], ['plan_bonus', 'Plan-covered bonus'], ['plan_cost_factor', 'Plan-covered cost factor (0-1)'], ['promo_bonus_max', 'Promo bonus at 100% off'], ['promo_min_remaining_hours', 'Ignore promos ending within (h)'], ['upcoming_lookahead_hours', 'Price-change lookahead (h)'], ['avoid_sunset_days', 'Avoid models sunsetting within (days)'],
        ['new_model_days', 'A model is "new" for (days)'], ['canary_percent', 'Canary traffic to new models (%)'], ['canary_min_requests', 'Requests to prove a model'], ['canary_min_success', 'Success rate to prove (0-1)'], ['sticky_minutes', 'Session stickiness (min)'], ['stats_window_hours', 'Health window (h)']].map(([k, l]) => field(R, k, { type: 'num', label: l })))),
    h('div', { class: 'card' }, h('h3', {}, 'Resilience'), h('div', { class: 'grid' }, [['max_attempts', 'Max attempts per request'], ['cooldown_base_s', 'Cooldown base (s)'], ['cooldown_max_s', 'Cooldown max (s)'], ['breaker_failures', 'Breaker opens after failures'], ['breaker_open_s', 'Breaker open time (s)']].map(([k, l]) => field(c.resilience, k, { type: 'num', label: l })))),
    h('div', { class: 'card' }, h('div', { class: 'row spread' }, h('h3', {}, 'JEV quality judge (System One)'), h('span', {}, chip(j.enabled ? 'enabled' : 'disabled', j.enabled ? 'ok' : ''), chip(`today Rp${idr(j.spent_idr)} / Rp${idr(j.budget_idr)}`))),
      h('p', { class: 'muted' }, 'Candidates answer the prompts below; JEV scores each answer 0-1. The average feeds the ranking and gates unproven models. Calls are capped by the daily budget.'),
      h('div', { class: 'grid' }, field(c.jev, 'enabled', { type: 'bool' }), field(c.jev, 'model', { label: 'Judge model' }), field(c.jev, 'upstream', { label: 'Upstream to use', help: 'empty = any usable' }), field(c.jev, 'daily_budget_idr', { type: 'num', label: 'Daily budget (Rp)' }), field(c.jev, 'every_hours', { type: 'num', label: 'Re-evaluate every (h)' }), field(c.jev, 'pass_threshold', { type: 'num', step: '0.01', label: 'Pass threshold (0-1)' })),
      field(c.jev, 'prompts', { type: 'lines', label: 'Golden prompts (one per line)' }),
      h('div', { class: 'row', style: 'margin:8px 0' }, h('button', { onclick: () => api('jev/run', { method: 'POST', body: {} }).then(() => toast('Evaluation started in the background'), fail) }, 'Evaluate due models now')),
      Object.keys(j.scores || {}).length ? h('div', { class: 'scroll' }, h('table', {}, h('thead', {}, h('tr', {}, ['model', 'score', 'prompts', 'age'].map(x => h('th', {}, x)))), h('tbody', {}, Object.entries(j.scores).sort((a, b) => b[1].Score - a[1].Score).map(([m, s]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', {}, s.Score.toFixed(2)), h('td', {}, s.N), h('td', {}, ago(new Date(s.TS)))))))) : h('p', { class: 'muted' }, 'No scores yet.')),
    h('div', { class: 'card' }, h('h3', {}, 'RTK token saver'), h('p', { class: 'muted' }, 'Compresses large tool outputs (git diff, grep, ls, logs) before they are sent upstream. Never touches user or assistant text. Per request: header X-Xellter-RTK: off.'),
      h('div', { class: 'grid' }, field(c.rtk, 'enabled', { type: 'bool' }), field(c.rtk, 'git_diff', { type: 'bool' }), field(c.rtk, 'grep', { type: 'bool' }), field(c.rtk, 'ls', { type: 'bool' }), field(c.rtk, 'logs', { type: 'bool' }), field(c.rtk, 'truncate', { type: 'bool' }),
        field(c.rtk, 'min_chars', { type: 'num', label: 'Only outputs longer than (chars)' }), field(c.rtk, 'max_chars', { type: 'num', label: 'Hard cap (chars)' }), field(c.rtk, 'diff_context', { type: 'num', label: 'Diff context lines' }))),
    h('div', { class: 'card' }, h('div', { class: 'row spread' }, h('h3', {}, 'Benchmark scores (0-100)'), h('button', { onclick: () => { const m = prompt('Model id:'); if (m) { c.benchmarks[m.trim()] = 50; markDirty(); render(); } } }, '+ Add')),
      h('p', { class: 'muted' }, 'Overrides the price-based proxy. Put a score from a benchmark that matches your use case (agentic tool use, instruction following, Indonesian).'),
      h('datalist', { id: 'models' }, cat.map(m => h('option', { value: m.id }))),
      benchRows.length ? h('div', { class: 'scroll' }, h('table', {}, h('tbody', {}, benchRows.map(([m, v]) => h('tr', {}, h('td', { class: 'mono' }, m), h('td', {}, h('input', { type: 'number', min: 0, max: 100, value: v, onchange: e => { c.benchmarks[m] = Number(e.target.value); markDirty(); } })), h('td', {}, h('button', { class: 'danger ghost', onclick: () => { delete c.benchmarks[m]; markDirty(); render(); } }, 'Remove'))))))) : h('p', { class: 'muted' }, 'None set: the price proxy is used.')),
    h('div', { class: 'card' }, h('h3', {}, 'Server'), h('div', { class: 'grid' }, field(c.server, 'admin_hosts', { type: 'list', label: 'Hostnames allowed to open /admin' }), field(c.server, 'log_retention_days', { type: 'num', label: 'Keep request log (days)' }), field(c.server, 'session_hours', { type: 'num', label: 'Admin session (hours)' }), field(c.kenari, 'sync_minutes', { type: 'num', label: 'Catalog sync (min)' })),
      h('p', { class: 'help' }, 'Listen address and data paths are changed in the YAML file and need a restart.'),
      h('button', { class: 'ghost', onclick: () => api('logout-all', { method: 'POST' }).then(() => location.reload(), fail) }, 'Sign out everywhere')));
};

views.usage = async function (root) {
  const group = root._g || 'model', hours = root._h || 24;
  const [rows, reqs] = await Promise.all([api(`usage?group=${group}&hours=${hours}`), api('requests?limit=100')]);
  const sel = (opts, cur, on) => h('select', { style: 'width:auto', onchange: e => on(e.target.value) }, opts.map(o => h('option', { value: o, selected: String(o) === String(cur) }, o)));
  root.replaceChildren(h('div', { class: 'row' }, h('h2', { style: 'margin:0' }, 'Usage'), sel(['model', 'client', 'upstream', 'shadow', 'hour'], group, v => { root._g = v; render(); }), sel([1, 6, 24, 72, 168, 720], hours, v => { root._h = +v; render(); }), h('span', { class: 'muted' }, 'hours')),
    h('div', { class: 'card scroll' }, h('table', {}, h('thead', {}, h('tr', {}, [group, 'requests', 'errors', 'in tok', 'out tok', 'avg ms', 'Rp (catalog)'].map((x, i) => h('th', { class: i ? 'num' : '' }, x)))),
      h('tbody', {}, rows.map(r => h('tr', {}, h('td', { class: 'mono' }, r.key || '–'), h('td', { class: 'num' }, r.requests), h('td', { class: 'num' }, r.errors), h('td', { class: 'num' }, r.in_tok), h('td', { class: 'num' }, r.out_tok), h('td', { class: 'num' }, Math.round(r.avg_latency_ms)), h('td', { class: 'num' }, idr(r.cost_idr))))))),
    h('h2', {}, 'Recent requests'),
    h('div', { class: 'card scroll' }, h('table', {}, h('thead', {}, h('tr', {}, ['age', 'client', 'asked', 'served by', 'key', 'status', 'ms', 'ttft', 'in/out', 'Rp', 'billing', 'notes'].map(x => h('th', {}, x)))),
      h('tbody', {}, reqs.map(r => h('tr', {}, h('td', {}, ago(r.ts)), h('td', {}, r.client), h('td', { class: 'mono' }, r.requested || r.shadow), h('td', { class: 'mono' }, r.model), h('td', {}, r.upstream), h('td', { class: r.ok ? 'ok' : 'bad' }, r.status || 'err'),
        h('td', { class: 'num' }, r.latency_ms), h('td', { class: 'num' }, r.ttft_ms || ''), h('td', { class: 'num' }, `${r.in_tok}/${r.out_tok}`), h('td', { class: 'num' }, idr(r.cost_idr)), h('td', {}, r.billing),
        h('td', {}, r.attempt > 1 && chip('attempt ' + r.attempt, 'warn'), r.canary && chip('canary'), r.rtk_saved > 0 && chip('rtk -' + r.rtk_saved), r.err && h('span', { class: 'help bad' }, r.err))))))));
};

views.config = async function (root) {
  await loadCfg();
  const ta = h('textarea', { style: 'min-height:420px', spellcheck: 'false' }); ta.value = S.yaml;
  root.replaceChildren(h('h2', {}, 'Raw config'), h('p', { class: 'muted' }, 'The same data as the other tabs, as YAML. Saving validates first; the previous version is kept in history and can be restored.'),
    h('div', { class: 'card' }, ta, h('div', { class: 'row', style: 'margin-top:8px' },
      h('button', { class: 'primary', onclick: async () => { try { await api('config', { method: 'PUT', body: { yaml: ta.value } }); toast('Saved and applied'); render(); } catch (e) { fail(e); } } }, 'Validate, save & apply'),
      h('button', { class: 'ghost', onclick: () => render() }, 'Reload'))),
    h('h3', {}, 'History'),
    h('div', { class: 'card' }, S.history.length ? S.history.slice(0, 20).map(id => h('div', { class: 'row spread', style: 'padding:4px 0' }, h('span', { class: 'mono' }, id), h('button', { onclick: async () => { if (!confirm('Restore ' + id + '?')) return; try { await api('rollback', { method: 'POST', body: { id } }); toast('Restored'); render(); } catch (e) { fail(e); } } }, 'Restore'))) : h('span', { class: 'muted' }, 'No earlier versions yet.')));
};

async function ensureCfg() { if (!S.cfg || !S.dirty) await loadCfg(); }

// ---------- shell ----------
async function render() {
  const root = $('#view');
  [...$('#nav').children].forEach(b => b.classList.toggle('on', b.dataset.tab === S.tab));
  clearInterval(S.timer);
  try {
    const fresh = h('div');
    await views[S.tab](fresh);
    root.replaceChildren(...fresh.childNodes);
    fresh._g && (root._g = fresh._g);
  } catch (e) { if (e.message !== 'login required') root.replaceChildren(h('p', { class: 'bad' }, e.message)); }
  if (S.tab === 'overview') S.timer = setInterval(() => { if (!document.hidden && !$('#dlg').open) render(); }, 10000);
}

function showLogin() { $('#app').hidden = true; $('#login').hidden = false; }
function showApp() { $('#login').hidden = true; $('#app').hidden = false; }

$('#nav').append(...TABS.map(([id, label]) => h('button', { 'data-tab': id, onclick: () => { if (S.dirty && !confirm('Discard unsaved changes?')) return; S.dirty = false; $('#savebar').hidden = true; S.cfg = null; S.tab = id; render(); } }, label)));
$('#save').onclick = saveCfg;
$('#discard').onclick = async () => { await loadCfg(); render(); };
$('#logout').onclick = () => api('logout', { method: 'POST' }).then(showLogin, fail);
$('#loginform').addEventListener('submit', async e => {
  e.preventDefault(); const f = new FormData(e.target); $('#loginerr').textContent = '';
  try { await api('login', { method: 'POST', body: { username: f.get('username'), password: f.get('password'), code: f.get('code') } }); e.target.reset(); showApp(); render(); }
  catch (err) { $('#loginerr').textContent = err.message; }
});
window.addEventListener('beforeunload', e => { if (S.dirty) { e.preventDefault(); e.returnValue = ''; } });
api('me').then(() => { showApp(); render(); }, () => showLogin());
