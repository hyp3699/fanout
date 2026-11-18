const $ = s => document.querySelector(s);
const ICON = {
  copy:'<svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>',
  stop:'<svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>',
  redo:'<svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></svg>',
  ok:'<svg viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg>',
  bad:'<svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
  run:'<svg viewBox="0 0 24 24" class="spin"><path d="M21 12a9 9 0 1 1-6.2-8.5"/></svg>',
  wait:'<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/></svg>',
  plus:'<svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>',
  trash:'<svg viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
  x:'<svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
  lock:'<svg viewBox="0 0 24 24" class="lock"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>'
};

// 界面挂在随机前缀下，请求一律走相对路径
async function api(path, opts){
  const r = await fetch(path.replace(/^\//, ''), opts);
  const d = await r.json().catch(()=>({}));
  if(!r.ok) throw new Error(d.error || ('HTTP '+r.status));
  return d;
}
function esc(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, c =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

let toastTimer;
function toast(msg, bad){
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast show' + (bad ? ' bad' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = 'toast'; }, 2400);
}
async function copy(text){
  // navigator.clipboard 只在 HTTPS/localhost 下存在，而面板通常是 http://IP 访问，
  // 所以必须留一条 execCommand 兜底路径，否则复制在正常使用场景里必然失败。
  if(navigator.clipboard && window.isSecureContext){
    try{ await navigator.clipboard.writeText(text); toast('已复制'); return; }
    catch(e){}
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  // 放在视口内但不可见：置于视口外会让 iOS 在聚焦时滚动页面
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;padding:0;border:0';
  document.body.appendChild(ta);
  const prev = document.activeElement;
  ta.focus();
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try{ ok = document.execCommand('copy'); }catch(e){}
  ta.remove();
  if(prev && prev.focus) prev.focus();
  toast(ok ? '已复制' : '复制失败，请手动选中', !ok);
}

let view = {exits:[], inbounds:[], rules:[], panel:'', backend:'', public_ip:''};

// 后端是 fanout 管理的 sing-box。fanout 自己只管出口（socks 出站）和分流规则。
function backendName(){ return 'sing-box'; }

const STATUS = {up:'已连通', starting:'连接中', failed:'失败', stopped:'已停止'};

function renderExits(){
  const list = $('#list');
  const n = view.exits.length;
  $('#ecount').textContent = n ? n + ' 个' : '';
  $('#exportAll').disabled = !(view.inbounds || []).length;
  $('#stopall').disabled = !n;

  if(!n){
    list.innerHTML = '<div class="empty">还没有出口'
      + '<div><button class="primary" id="newexit2">'
      + '<svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>'
      + '新建出口</button></div></div>';
    return;
  }

  list.innerHTML = view.exits.map(e => {
    const label = e.exit_ip || (e.status === 'starting' ? '连接中…' : '—');
    // 出口这一行列出指向它的分流规则，点开即编辑
    const chips = (e.rules || []).length
      ? e.rules.map(r => '<button class="chip" data-rule="' + r.id + '" title="分流规则'
          + (r.enabled ? '' : '（已停用）') + '"' + (r.enabled ? '' : ' style="opacity:.5"') + '>'
          + esc(r.name || ('规则 #' + r.id)) + '</button>').join('')
      : '<span class="chip none">无规则</span>';
    const err = e.status === 'failed' && e.err
      ? '<div class="errline" title="' + esc(e.err) + '">' + esc(e.err) + '</div>' : '';
    // country 后端已经给成"国旗 中文"，再拼国家码就重复了
    const place = esc(e.country || e.region || '—');
    return '<div class="exit">'
      + '<div class="row">'
      +   '<span class="dot ' + e.status + '" title="' + (STATUS[e.status] || e.status) + '"></span>'
      +   '<span class="ip">' + esc(label) + '</span>'
      +   '<span class="meta">' + place + ' · ' + esc(e.host) + '</span>'
      +   '<span class="chips">' + chips + '</span>'
      +   '<span class="socks"><button data-cred="' + e.slot + '" title="SOCKS5 访问凭据">'
      +     ICON.lock + ':' + e.port + '</button></span>'
      +   '<span class="acts">'
      +     '<button class="icon" data-swap="' + e.slot + '" title="换一个节点">' + ICON.redo + '</button>'
      +     '<button class="icon" data-stop="' + e.slot + '" title="停止这个出口">' + ICON.stop + '</button>'
      +   '</span>'
      + '</div>' + err + '</div>';
  }).join('');
}

// ---- 分流规则列表 ----
const RSTATE = {up:'出口已连通', down:'出口还没连通，规则暂不生效', gone:'出口已停掉，规则不生效（编辑换一个出口）'};
const OSTATE = {up:'已有出站，始终生效', gone:'配置目录里已经没有这个出站，规则不生效（编辑换一个目标）'};
function stateTip(r){ return (r.target === 'outbound' ? OSTATE : RSTATE)[r.exit_state] || ''; }

function shortRS(src){
  if(/^geo(site|ip):/i.test(src)) return src;
  try{ const u = new URL(src); return u.pathname.split('/').pop() || u.host; }catch(e){ return src; }
}
function ruleCond(r){
  if(r.all) return '全部流量';
  const parts = [];
  const d = r.domains || [];
  if(d.length) parts.push(d.slice(0, 3).join(' ') + (d.length > 3 ? ' 等 ' + d.length + ' 条' : ''));
  const s = (r.local_rule_sets || []).concat((r.rule_sets || []).map(x => shortRS(x.source)));
  if(s.length) parts.push('规则集 ' + s.join(' '));
  return parts.join(' ｜ ');
}

function renderRules(){
  const box = $('#rules');
  const list = view.rules || [];
  $('#rcount').textContent = list.length ? list.length + ' 条' : '';
  if(!list.length){
    box.innerHTML = '<div class="empty small">还没有分流规则。新建一条：选用户，填域名或规则集，选出口——只有命中的流量走出口。</div>';
    return;
  }
  box.innerHTML = list.map((r, i) => {
    const dot = !r.enabled ? 'off' : (r.active ? 'up' : 'failed');
    const tip = !r.enabled ? '已停用' : (r.active ? '生效中' : (stateTip(r) || '不生效'));
    const miss = new Set(r.missing_users || []);
    const ins = (r.users || []).map(t => '<span class="chip static' + (miss.has(t) ? ' miss' : '') + '"'
      + (miss.has(t) ? ' title="配置目录里的入站已经没有这个用户"' : '') + '>' + esc(t) + '</span>').join('');
    const name = r.name || ('规则 #' + r.id);
    return '<div class="rule' + (r.enabled ? '' : ' off') + '">'
      + '<span class="dot ' + dot + '" title="' + esc(tip) + '"></span>'
      + '<span class="rno">' + (i + 1) + '</span>'
      + '<span class="rname">' + esc(name) + '</span>'
      + '<span class="chips">' + ins + '</span>'
      + '<span class="rcond" title="' + esc(ruleCond(r)) + '">' + esc(ruleCond(r)) + '</span>'
      + '<span class="rexit' + (r.exit_state === 'up' ? '' : ' bad') + '" title="' + esc(stateTip(r)) + '">→ '
      +   esc(r.exit_label || r.outbound || r.exit)
      +   (r.exit_state === 'gone' ? (r.target === 'outbound' ? '（已不存在）' : '（已停）') : '') + '</span>'
      + '<span class="acts">'
      +   '<button class="icon" data-rmove="' + r.id + '" data-dir="up" title="上移"' + (i === 0 ? ' disabled' : '') + '>'
      +     '<svg viewBox="0 0 24 24"><path d="m18 15-6-6-6 6"/></svg></button>'
      +   '<button class="icon" data-rmove="' + r.id + '" data-dir="down" title="下移"' + (i === list.length - 1 ? ' disabled' : '') + '>'
      +     '<svg viewBox="0 0 24 24"><path d="m6 9 6 6 6-6"/></svg></button>'
      +   '<button class="icon" data-renable="' + r.id + '" data-on="' + (r.enabled ? '0' : '1') + '" title="' + (r.enabled ? '停用' : '启用') + '">'
      +     (r.enabled ? ICON.stop : ICON.ok) + '</button>'
      +   '<button class="icon" data-rule="' + r.id + '" title="编辑">'
      +     '<svg viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg></button>'
      +   '<button class="icon danger" data-rdel="' + r.id + '" data-name="' + esc(name) + '" title="删除">' + ICON.trash + '</button>'
      + '</span></div>';
  }).join('');
}

// ---- 用户：限速 / 流量限制 ----
let users = {users:[], traffic_ready:false, speed_ready:false}, curUser = '';
const MODE = {bidirectional:'上传+下载', upload:'仅上传', download:'仅下载'};
const PERIOD = {none:'不重置', day:'每天重置', month:'每月重置'};

function fmtBytes(n){
  n = Number(n) || 0;
  const u = ['B','KB','MB','GB','TB']; let i = 0;
  while(n >= 1024 && i < u.length - 1){ n /= 1024; i++; }
  return (i ? n.toFixed(2).replace(/\.?0+$/, '') : n) + ' ' + u[i];
}
function trimN(v){ return String(Math.round(v * 100) / 100); }
// 与后端 speedToBps / speedText 一致：MB = 1000*1000 字节/秒，Mbps = MB/8
function speedBps(input){
  const s = String(input || '').replace(/\s+/g, '');
  if(/^\d*\.?\d+$/.test(s)) return parseFloat(s) * 1e6;
  const m = s.match(/^(\d+)([A-Za-z]+)(\/[sS])?$/);
  if(!m) return 0;
  const n = Number(m[1]), u = m[2];
  const l = u.toLowerCase();
  if(l === 'kbps') return n * 1000 / 8;
  if(l === 'mbps') return n * 1e6 / 8;
  if(l === 'gbps') return n * 1e9 / 8;
  if(l === 'k' || l === 'kb') return n * 1000;
  if(l === 'm' || l === 'mb') return n * 1e6;
  if(l === 'g' || l === 'gb') return n * 1e9;
  return 0;
}
function speedLabel(bps){
  const a = bps >= 1e6 ? trimN(bps / 1e6) + ' MB/s' : bps >= 1000 ? trimN(bps / 1000) + ' KB/s' : Math.round(bps) + ' B/s';
  const m = bps * 8 / 1e6;
  return a + '（实际网速 ' + (m >= 1 ? trimN(m) + ' Mbps' : trimN(bps * 8 / 1000) + ' Kbps') + '）';
}
function trafText(u){
  const t = u.traffic;
  if(!t) return '未设置';
  if(t.disabled_by_limit && !t.enabled) return '已停用';
  return fmtBytes(u.period_total) + ' / ' + fmtBytes(t.limit_bytes) + ' · ' + (PERIOD[t.period] || '不重置')
    + (t.disabled_by_limit ? ' · 已停用' : '');
}

function renderUsers(){
  const box = $('#users');
  const list = users.users || [];
  $('#ucount').textContent = list.length ? list.length + ' 个' : '';
  if(!list.length){
    box.innerHTML = '<div class="empty small">配置目录里的入站还没有用户。</div>';
    return;
  }
  box.innerHTML = list.map(u => {
    const ins = u.missing
      ? '<span class="chip static miss" title="不在任何入站里（可能已到量停用）">不在入站</span>'
      : (u.inbounds || []).map(t => '<span class="chip static">' + esc(t) + '</span>').join('');
    const sp = u.speed ? (u.speed.text + ' ' + (MODE[u.speed.mode] || '')) : '未设置';
    const tbad = u.traffic && u.traffic.disabled_by_limit;
    return '<div class="rule">'
      + '<span class="rname">' + esc(u.name) + '</span>'
      + '<span class="chips">' + ins + '</span>'
      + '<span class="rcond">已用 ' + esc(fmtBytes(u.total)) + (u.rules ? ' ｜ 分流 ' + u.rules + ' 条' : '') + '</span>'
      + '<span class="uinfo' + (tbad ? ' bad' : '') + '">流量 ' + esc(trafText(u)) + '</span>'
      + '<span class="uinfo">限速 ' + esc(sp) + '</span>'
      + '<span class="acts"><button class="icon" data-user="' + esc(u.name) + '" title="限速 / 流量限制">'
      +   '<svg viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg></button></span>'
      + '</div>';
  }).join('');
}

function calcSpeedHint(){
  const v = $('#uspeed').value.trim();
  const bps = speedBps(v);
  $('#uspeedcalc').textContent = v && bps > 0
    ? '填 ' + v + ' → ' + speedLabel(bps)
    : '1 MB/s = 8 Mbps：填 1，实际网速就是 8 Mbps';
}
$('#uspeed').oninput = calcSpeedHint;

function fillUser(){
  const u = (users.users || []).find(x => x.name === curUser);
  if(!u) return;
  $('#utitle').textContent = '用户 ' + u.name;
  $('#uspeedcur').textContent = '当前限速：' + (u.speed ? u.speed.text + ' ' + (MODE[u.speed.mode] || '') : '未设置')
    + (u.rules ? '　｜　该用户有 ' + u.rules + ' 条分流规则，限速后照常生效' : '')
    + (users.speed_ready ? '' : '　｜　当前内核不支持限速，需要 -xhttp-limiter 版 sing-box');
  $('#utrafcur').textContent = (users.traffic_ready ? '' : '未检测到流量统计服务（需要先用 sb.sh 安装）　｜　')
    + '流量限制：' + trafText(u) + '　｜　本周期已用 ' + fmtBytes(u.period_total) + '，累计 ' + fmtBytes(u.total);
  $('#uspeeddel').disabled = !u.speed;
  $('#utrafdel').disabled = !u.traffic;
}

function openUser(name){
  curUser = name;
  const u = (users.users || []).find(x => x.name === name);
  if(!u){ toast('这个用户不在了', true); return; }
  $('#uspeed').value = u.speed ? (u.speed.speed.match(/^\d+MB$/) ? u.speed.speed.slice(0, -2) : u.speed.speed) : '';
  $('#umode').value = u.speed ? u.speed.mode : 'bidirectional';
  $('#ulimit').value = u.traffic && u.traffic.enabled
    ? (u.traffic.limit_unit === 'GB' ? String(u.traffic.limit_value) : u.traffic.limit_value + 'MB') : '';
  $('#uperiod').value = (u.traffic && u.traffic.period) || 'none';
  calcSpeedHint();
  fillUser();
  openModal('userbox');
}
document.addEventListener('click', e => {
  const b = e.target.closest('[data-user]');
  if(b) openUser(b.dataset.user);
});

async function userPost(path, body, btn, ok){
  btn.disabled = true;
  try{
    await api(path, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
    users = await api('/api/users'); renderUsers(); fillUser();
    toast(ok);
  }catch(err){ toast(err.message, true); }
  btn.disabled = false;
}
$('#uspeedsave').onclick = e => {
  const v = $('#uspeed').value.trim();
  if(!v || !(speedBps(v) > 0)){ toast('请输入限速，例如 10、0.5、500KB、100Mbps', true); return; }
  userPost('/api/users/speed', {name: curUser, speed: v, mode: $('#umode').value}, e.target, '限速已保存');
};
$('#uspeeddel').onclick = e => {
  if(!confirm('取消 ' + curUser + ' 的限速？')) return;
  $('#uspeed').value = ''; calcSpeedHint();
  userPost('/api/users/speed', {name: curUser, speed: ''}, e.target, '已取消限速');
};
$('#utrafsave').onclick = e => {
  const v = $('#ulimit').value.trim();
  if(!v || v === '0'){ toast('请输入流量限制，例如 2（=2GB）、500MB', true); return; }
  userPost('/api/users/traffic', {name: curUser, limit: v, period: $('#uperiod').value}, e.target, '流量限制已保存，本周期流量已清零');
};
$('#utrafdel').onclick = e => {
  if(!confirm('取消 ' + curUser + ' 的流量限制？')) return;
  $('#ulimit').value = '';
  userPost('/api/users/traffic', {name: curUser, limit: '0'}, e.target, '已取消流量限制');
};

// ---- 入站（只读） ----
function renderJobs(jobs){
  const box = $('#jobs');
  box.innerHTML = jobs.map(j => {
    const steps = j.steps.map(s => {
      const ic = {ok:ICON.ok, failed:ICON.bad, running:ICON.run}[s.status] || ICON.wait;
      const t = s.detail ? s.label + ' — ' + s.detail : s.label;
      return '<span class="step ' + s.status + '" title="' + esc(t) + '">' + ic
        + esc(s.status === 'ok' && s.detail ? s.detail : s.label) + '</span>';
    }).join('');
    const close = j.status === 'running' ? ''
      : '<button class="icon" data-job="' + esc(j.id) + '" title="关闭">' + ICON.x + '</button>';
    return '<div class="job"><div class="top"><strong>' + esc(j.summary) + '</strong>'
      + '<span class="count">' + j.done + '/' + j.total + '</span>'
      + '<span class="spacer"></span>' + close + '</div>'
      + '<div class="steps">' + steps + '</div></div>';
  }).join('');
}

async function poll(){
  try{
    view = await api('/api/exits');
    $('#panel').textContent = view.panel
      ? (backendName() + ': ' + view.panel)
      : (view.panel_info || '');
    renderExits();
    renderRules();
    if(typeof onPollTick==='function') onPollTick();
  }catch(e){}
  try{ users = await api('/api/users'); renderUsers(); }catch(e){}
  try{ renderJobs(await api('/api/jobs') || []); }catch(e){}
}

// ---- 新建向导 ----
let regions = [], region = '', regionsLoaded = false;

function openModal(id){ $('#' + id).classList.add('open'); }
function closeModal(id){ $('#' + id).classList.remove('open'); }

document.addEventListener('click', e => {
  const c = e.target.closest('[data-close]');
  if(c) closeModal(c.dataset.close);
});
document.addEventListener('keydown', e => {
  if(e.key === 'Escape') document.querySelectorAll('.modal.open')
    .forEach(m => m.classList.remove('open'));
});
document.querySelectorAll('.modal').forEach(m => {
  m.onclick = e => { if(e.target === m) m.classList.remove('open'); };
});

function renderRegions(){
  const kw = $('#rgfilter').value.trim().toLowerCase();
  const list = regions.filter(r => !kw
    || r.code.toLowerCase().includes(kw) || r.name.toLowerCase().includes(kw));
  $('#regions').innerHTML = ['<button class="rg' + (region === '' ? ' sel' : '')
      + '" data-rg=""><b>不限地区</b><em>速度优先</em></button>',
    '<button class="rg' + (region === '*' ? ' sel' : '')
      + '" data-rg="*"><b>每个国家</b><em>' + regions.length + ' 个国家各来几个</em></button>']
    .concat(list.map(r => '<button class="rg' + (region === r.code ? ' sel' : '')
      + '" data-rg="' + esc(r.code) + '"><b>' + esc(r.name || r.code) + '</b>'
      + '<em>' + r.available + ' 个空闲 · ' + r.best_speed_mbps.toFixed(0) + ' Mbps</em></button>'))
    .join('');
  updateAvail();
}

function availOf(code){
  if(code === '') return regions.reduce((a, r) => a + r.available, 0);
  if(code === '*') return regions.reduce((a, r) => a + r.available, 0);
  const r = regions.find(x => x.code === code);
  return r ? r.available : 0;
}

function updateAvail(){
  const want = Number($('#count').value) || 0;
  const hint = $('#availhint');
  // 选了"每个国家"时，数量的意思是每国几个，提示要给出总条数
  $('#countlabel').textContent = region === '*' ? '每个国家几个' : '数量';
  if(region === '*'){
    const n = regions.length;
    const total = Math.min(n * want, availOf('*'));
    hint.className = 'hint';
    hint.textContent = n
      ? n + ' 个国家 × ' + want + '，一共 ' + total + ' 条出口'
      : '还没有可用节点';
    $('#go').disabled = !n || !want;
    return;
  }
  const avail = availOf(region);
  hint.textContent = avail ? '可用 ' + avail + ' 个节点' : '这个地区没有空闲节点';
  hint.className = 'hint' + (want > avail ? ' bad' : '');
  if(want > avail && avail) hint.textContent = '只剩 ' + avail + ' 个，将全部使用';
  $('#go').disabled = !avail;
}

async function loadWizard(){
  try{
    regions = await api('/api/regions') || [];
    regionsLoaded = true;
    renderRegions();
  }catch(e){ toast('读取地区失败: ' + e.message, true); }

}

document.addEventListener('click', e => {
  if(e.target.closest('#newexit') || e.target.closest('#newexit2')){
    openModal('wizard');
    if(!regionsLoaded) loadWizard(); else { renderRegions(); loadWizard(); }
  }
  const rg = e.target.closest('[data-rg]');
  if(rg){
    region = rg.dataset.rg;
    // "每个国家"是批量，默认每国 1 个，免得一点就开出几十条
    if(region === '*' && Number($('#count').value) > 3) $('#count').value = '1';
    renderRegions();
  }
});

$('#rgfilter').oninput = renderRegions;
$('#minus').onclick = () => { step(-1); };
$('#plus').onclick = () => { step(1); };
function step(d){
  const el = $('#count');
  el.value = Math.min(20, Math.max(1, (Number(el.value) || 1) + d));
  updateAvail();
}
$('#count').oninput = updateAvail;

$('#go').onclick = async e => {
  const want = Math.min(Number($('#count').value) || 1, availOf(region) || 1);
  e.target.disabled = true;
  try{
    // 只开出口，不建入站；想让哪些流量走新出口，去「分流规则」里建规则
    await api('/api/provision?count=' + want
      + (region === '*' ? '&every=1' : '&region=' + encodeURIComponent(region)), {method:'POST'});
    closeModal('wizard');
    poll();
  }catch(err){ toast(err.message, true); }
  e.target.disabled = false;
};

// ---- 出口操作 ----
document.addEventListener('click', async e => {
  const stop = e.target.closest('[data-stop]');
  if(stop){
    stop.disabled = true;
    try{ await api('/api/stop?slot=' + stop.dataset.stop, {method:'POST'}); }
    catch(err){ toast(err.message, true); }
    poll();
    return;
  }
  const swap = e.target.closest('[data-swap]');
  if(swap){
    swap.disabled = true;
    try{
      await api('/api/swap?slot=' + swap.dataset.swap, {method:'POST'});
      toast('正在换节点');
    }catch(err){ toast(err.message, true); }
    poll();
    return;
  }
  const cred = e.target.closest('[data-cred]');
  if(cred){ openCred(Number(cred.dataset.cred)); return; }
  const job = e.target.closest('[data-job]');
  if(job){
    try{ await api('/api/jobs/dismiss?id=' + job.dataset.job, {method:'POST'}); }catch(err){}
    poll();
    return;
  }
});

$('#stopall').onclick = async e => {
  if(!confirm('停止全部 ' + view.exits.length + ' 个出口？')) return;
  e.target.disabled = true;
  for(const x of view.exits){
    try{ await api('/api/stop?slot=' + x.slot, {method:'POST'}); }catch(err){}
  }
  poll();
};

// ---- 入站详情（只读） ----
async function openDetail(id){
  $('#dbody').innerHTML = '<div class="empty">读取中…</div>';
  openModal('detail');
  try{
    renderDetail(await api('/api/inbounds/detail?id=' + id));
  }catch(err){
    $('#dbody').innerHTML = '<div class="empty">读取失败: ' + esc(err.message) + '</div>';
  }
}

function renderDetail(d){
  $('#dtitle').textContent = d.tag + '　:' + d.port;
  const dusers = d.users || [];
  const rules = (view.rules || []).filter(r => (r.users || []).some(u => dusers.includes(u)));
  const links = d.links || [];
  $('#dbody').innerHTML = '<dl class="kv">'
    + '<dt>协议</dt><dd>' + esc(d.protocol) + '　' + esc(d.network || '')
    +   (d.tls && d.tls !== 'none' ? '　' + esc(d.tls) : '') + '</dd>'
    + '<dt>监听</dt><dd>' + esc(d.listen || '::') + ' :' + d.port + '</dd>'
    + '<dt>来源</dt><dd>' + esc(d.source || '') + '</dd>'
    + '<dt>用户</dt><dd>' + ((d.clients || []).map(c => esc(c.email)).join('、') || '<span class="dim">—</span>') + '</dd>'
    + '<dt>分流</dt><dd>' + (rules.length
        ? rules.map(r => '<button class="chip" data-rule="' + r.id + '">' + esc(r.name || ('规则 #' + r.id)) + '</button>').join(' ')
        : '<span class="dim">没有规则，全部流量按原路由走</span>') + '</dd>'
    + '</dl>'
    + '<div class="chead"><h3>分享链接</h3><span class="count">' + links.length + ' 条</span></div>'
    + (links.length ? links.map(l => '<div class="client"><div class="crow"><span class="spacer"></span>'
        + '<button class="icon" data-copy="' + esc(l) + '" title="复制链接">' + ICON.copy + '</button></div>'
        + '<div class="share">' + esc(l) + '</div></div>').join('')
      : '<div class="empty small">这个协议推不出分享链接</div>');
}

document.addEventListener('click', e => {
  const link = e.target.closest('[data-detail]');
  if(link) openDetail(link.dataset.detail);
});

// ---- 分流规则编辑 ----
let curRule = null;

function rsRow(src, fmt){
  return '<div class="rsrow"><input type="text" class="rssrc" spellcheck="false"'
    + ' placeholder="geosite:netflix / geoip:jp / https://…/x.srs" value="' + esc(src || '') + '">'
    + '<select class="rsfmt"><option value="">自动</option>'
    + '<option value="binary"' + (fmt === 'binary' ? ' selected' : '') + '>binary</option>'
    + '<option value="source"' + (fmt === 'source' ? ' selected' : '') + '>source</option></select>'
    + '<button class="icon danger" data-rsdel="1" title="移除">' + ICON.trash + '</button></div>';
}

function syncRuleForm(){
  const all = $('#rall').checked;
  $('#rconds').style.opacity = all ? '.4' : '';
  $('#rconds').querySelectorAll('input,textarea,select,button').forEach(x => { x.disabled = all; });
}
$('#rall').onchange = syncRuleForm;

function openRule(id){
  const r = id ? (view.rules || []).find(x => x.id === Number(id)) : null;
  if(id && !r){ toast('这条规则不在了', true); return; }
  curRule = r;
  $('#rtitle').textContent = r ? '编辑分流规则' : '新建分流规则';
  $('#rname').value = r ? (r.name || '') : '';

  // 所有入站里的用户名（去重），后面标出它在哪些入站里
  const sel = new Set(r ? (r.users || []) : []);
  const umap = new Map();
  (view.inbounds || []).forEach(i => (i.users || []).forEach(u => {
    if(!umap.has(u)) umap.set(u, []);
    umap.get(u).push(i.tag);
  }));
  const missing = r ? (r.missing_users || []) : [];
  $('#rins').innerHTML = (umap.size || missing.length)
    ? Array.from(umap.entries()).map(([u, tags]) => '<label class="chk"><input type="checkbox" value="' + esc(u) + '"'
        + (sel.has(u) ? ' checked' : '') + '> ' + esc(u)
        + ' <span class="dim">' + esc(tags.join(', ')) + '</span></label>').join('')
      + missing.map(t => '<label class="chk" title="配置目录里的入站已经没有这个用户，保存时会去掉">'
        + '<input type="checkbox" disabled> <s>' + esc(t) + '</s></label>').join('')
    : '<span class="dim">配置目录里的入站还没有用户</span>';

  // 目标：fanout 的出口，或配置目录里已有的出站 / 端点。value 带前缀区分两类
  const exits = view.exits || [];
  let eopts = exits.map(e => '<option value="exit:' + esc(e.host) + '"'
    + (r && r.target !== 'outbound' && r.exit_host === e.host ? ' selected' : '') + '>'
    + esc((e.label || e.host)
      + (e.exit_ip && !(e.label || '').includes(e.exit_ip) ? ' · 出口 ' + e.exit_ip : '')
      + ' · ' + (STATUS[e.status] || e.status))
    + '</option>').join('');
  if(r && r.target !== 'outbound' && r.exit_state === 'gone'){
    eopts = '<option value="exit:' + esc(r.exit) + '" selected>（已停掉）' + esc(r.exit) + '</option>' + eopts;
  }
  const outs = view.outbounds || [];
  let oopts = outs.map(o => '<option value="out:' + esc(o.tag) + '"'
    + (r && r.outbound === o.tag ? ' selected' : '') + '>'
    + esc(o.tag + ' · ' + o.type + (o.endpoint ? ' 端点' : '') + ' · ' + o.file) + '</option>').join('');
  if(r && r.target === 'outbound' && r.exit_state === 'gone'){
    oopts = '<option value="out:' + esc(r.outbound) + '" selected>（已不存在）' + esc(r.outbound) + '</option>' + oopts;
  }
  $('#rexit').innerHTML = (eopts ? '<optgroup label="fanout 出口">' + eopts + '</optgroup>' : '')
    + (oopts ? '<optgroup label="已有出站">' + oopts + '</optgroup>' : '')
    || '<option value="">还没有出口，先新建出口</option>';

  // 已有规则集：配置目录里别的文件定义的 route.rule_set，直接按 tag 引用
  const lsel = new Set(r ? (r.local_rule_sets || []) : []);
  const lsets = view.rule_sets || [];
  const lmiss = r ? (r.missing_rule_sets || []) : [];
  $('#rlocal').innerHTML = (lsets.length || lmiss.length)
    ? lsets.map(x => '<label class="chk" title="来自 ' + esc(x.file || '') + '"><input type="checkbox" value="' + esc(x.tag) + '"'
        + (lsel.has(x.tag) ? ' checked' : '') + '> ' + esc(x.tag)
        + ' <span class="dim">' + esc(x.type || '') + '</span></label>').join('')
      + lmiss.map(t => '<label class="chk" title="配置目录里已经没有这个规则集，保存时会去掉">'
        + '<input type="checkbox" disabled> <s>' + esc(t) + '</s></label>').join('')
    : '<span class="dim">配置目录里没有定义规则集</span>';

  $('#rall').checked = !!(r && r.all);
  $('#rdomains').value = r ? (r.domains || []).join('\n') : '';
  $('#rsets').innerHTML = (r ? (r.rule_sets || []) : []).map(x => rsRow(x.source, x.format)).join('');
  $('#rresolve').checked = !!(r && r.resolve_ip);
  $('#renabled').checked = r ? !!r.enabled : true;
  $('#rhint').textContent = '';
  $('#rhint').className = 'count';
  syncRuleForm();
  openModal('rulebox');
}

$('#rsadd').onclick = () => { $('#rsets').insertAdjacentHTML('beforeend', rsRow('', '')); };

$('#rsave').onclick = async e => {
  const all = $('#rall').checked;
  const body = {
    id: curRule ? curRule.id : 0,
    name: $('#rname').value.trim(),
    enabled: $('#renabled').checked,
    users: Array.from(document.querySelectorAll('#rins input:checked:not(:disabled)')).map(x => x.value),
    exit: $('#rexit').value.startsWith('exit:') ? $('#rexit').value.slice(5) : '',
    outbound: $('#rexit').value.startsWith('out:') ? $('#rexit').value.slice(4) : '',
    all: all,
    domains: all ? '' : $('#rdomains').value,
    rule_sets: all ? [] : Array.from(document.querySelectorAll('#rsets .rsrow')).map(row => ({
      source: row.querySelector('.rssrc').value.trim(),
      format: row.querySelector('.rsfmt').value,
    })).filter(x => x.source),
    local_rule_sets: all ? [] : Array.from(document.querySelectorAll('#rlocal input:checked:not(:disabled)')).map(x => x.value),
    resolve_ip: !all && $('#rresolve').checked,
  };
  const btn = e.target.closest('button');
  btn.disabled = true;
  $('#rhint').className = 'count';
  $('#rhint').textContent = body.rule_sets.length ? '正在下载校验规则集…' : '保存中…';
  try{
    await api('/api/rules/save', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify(body),
    });
    toast('已保存');
    closeModal('rulebox');
    poll();
  }catch(err){
    // 规则的报错常常很长（规则集地址、校验失败原因），留在弹窗里让人看全
    $('#rhint').className = 'count hint bad';
    $('#rhint').textContent = err.message;
  }
  btn.disabled = false;
};

document.addEventListener('click', async e => {
  if(e.target.closest('#newrule')){ openRule(0); return; }
  const rsdel = e.target.closest('[data-rsdel]');
  if(rsdel){ rsdel.closest('.rsrow').remove(); return; }
  const ed = e.target.closest('[data-rule]');
  if(ed){ closeModal('detail'); openRule(ed.dataset.rule); return; }

  const mv = e.target.closest('[data-rmove]');
  if(mv){
    mv.disabled = true;
    try{ await api('/api/rules/move?id=' + mv.dataset.rmove + '&dir=' + mv.dataset.dir, {method:'POST'}); }
    catch(err){ toast(err.message, true); }
    poll();
    return;
  }
  const en = e.target.closest('[data-renable]');
  if(en){
    en.disabled = true;
    try{
      await api('/api/rules/enable?id=' + en.dataset.renable + '&on=' + en.dataset.on, {method:'POST'});
      toast(en.dataset.on === '1' ? '已启用' : '已停用');
    }catch(err){ toast(err.message, true); }
    poll();
    return;
  }
  const del = e.target.closest('[data-rdel]');
  if(del){
    if(!confirm('删除分流规则「' + del.dataset.name + '」？')) return;
    del.disabled = true;
    try{
      await api('/api/rules/delete?id=' + del.dataset.rdel, {method:'POST'});
      toast('已删除');
    }catch(err){ toast(err.message, true); }
    poll();
  }
});

document.addEventListener('click', e => {
  const c = e.target.closest('[data-copy]');
  if(c) copy(c.dataset.copy);
});

// ---- SOCKS5 凭据 ----
let curCred = null;

function socksURL(host, port, user, pass){
  if(!user) return 'socks5://' + host + ':' + port;
  return 'socks5://' + user + ':' + pass + '@' + host + ':' + port;
}

// SOCKS5 端口监听在母机（跑 fanout 的这台服务器）上，客户端要连的是母机的
// 公网 IPv4，流量再从出口 IP 出去。出口 IP 是"出去以后"的地址，不能当连接地址。
// public_ip 是后端探测到的母机公网地址；探测不到才退回访问面板用的主机名。
function credHost(e){
  return view.public_ip || location.hostname || e.host;
}

function openCred(slot){
  const e = view.exits.find(x => x.slot === slot);
  if(!e){ toast('这个出口不在了', true); return; }
  curCred = {slot: slot, port: e.port, host: credHost(e)};
  $('#crtitle').textContent = (e.country || e.region) + ' · :' + e.port;
  $('#cruser').value = e.socks_user || '';
  $('#crpass').value = e.socks_pass || '';
  refreshCredURL();
  openModal('credbox');
}

function refreshCredURL(){
  if(!curCred) return;
  $('#crurl').textContent = socksURL(curCred.host, curCred.port,
    $('#cruser').value.trim(), $('#crpass').value.trim());
}
$('#cruser').oninput = refreshCredURL;
$('#crpass').oninput = refreshCredURL;

$('#crrand').onclick = () => {
  // 客户端和服务端都要能识别，只用无歧义、无需转义的字符
  const abc = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
  const gen = n => Array.from(crypto.getRandomValues(new Uint8Array(n)))
    .map(v => abc[v % abc.length]).join('');
  $('#cruser').value = 'fo' + gen(6);
  $('#crpass').value = gen(14);
  refreshCredURL();
};

$('#crcopy').onclick = () => { copy($('#crurl').textContent); };

$('#crsave').onclick = async e => {
  if(!curCred) return;
  const btn = e.target; btn.disabled = true;
  const q = new URLSearchParams({
    slot: curCred.slot,
    user: $('#cruser').value.trim(),
    pass: $('#crpass').value.trim(),
  });
  try{
    const r = await api('/api/cred?' + q, {method:'POST'});
    $('#cruser').value = r.user;
    $('#crpass').value = r.pass;
    refreshCredURL();
    toast('已保存，立即生效');
    poll();
  }catch(err){ toast(err.message, true); }
  btn.disabled = false;
};

// ---- 导出 ----
$('#exportAll').onclick = async () => {
  const ids = (view.inbounds || []).map(i => i.id);
  if(!ids.length){ toast('还没有入站可导出', true); return; }
  $('#exbox').value = '读取中…';
  $('#excount').textContent = '';
  openModal('export');
  try{
    const d = await api('/api/inbounds/links?ids=' + ids.join(','));
    $('#exbox').value = (d.links || []).join('\n');
    $('#excount').textContent = (d.links || []).length + ' 条';
  }catch(err){ $('#exbox').value = '导出失败: ' + err.message; }
};
$('#copyall').onclick = () => { const v = $('#exbox').value; if(v) copy(v); };

// ---- 设置：改密码 / 改路径 / 改端口 / 改本地监听 ----
let curSettings = null;

// 后端只有 sing-box 一种，设置里只显示它的状态
async function loadBackendModes(){
  const hint = $('#setBackendHint');
  try{
    const m = await api('/api/backend');
    hint.textContent = m.available
      ? ('当前：' + m.describe + '。fanout 只写配置目录里的 fanout-outbounds.json 和 fanout-route.json。')
      : ('sing-box 不可用：' + (m.reason || ''));
  }catch(err){ hint.textContent = err.message; }
}

$('#settingsBtn').onclick = async () => {
  $('#setPw').value = '';
  $('#setPath').value = '';
  $('#setPathHint').textContent = '读取中…';
  openModal('settings');
  loadBackendModes();
  try{
    const s = await api('/api/settings');
    curSettings = s;
    $('#setPath').value = (s.base_path || '').replace(/^\//, '');
    $('#setPort').value = s.port || '';
    $('#setListen').value = s.listen_addr || '0.0.0.0';
    $('#setResi').checked = s.residential_only !== false;
    $('#setPathHint').textContent = '界面挂在这个路径下，扫端口的探不到。只能用字母数字和 - _。';
    $('#updCur').textContent = s.version || '-';
    $('#updLatest').textContent = '';
    $('#updNotes').hidden = true;
    $('#updApply').hidden = true;
    $('#updCheck').disabled = false;
    $('#updCheck').textContent = '检查更新';
  }catch(err){ $('#setPathHint').textContent = '读取失败: ' + err.message; }
};

// 检查更新：问后端 GitHub 最新版，有新版就亮出更新按钮和更新内容
$('#updCheck').onclick = async e => {
  e.target.disabled = true;
  e.target.textContent = '检查中…';
  try{
    const u = await api('/api/update/check');
    $('#updCur').textContent = u.current || '-';
    if(u.has_update){
      $('#updLatest').textContent = '有新版本 ' + u.latest;
      $('#updApplyVer').textContent = u.latest;
      $('#updApply').hidden = false;
      $('#updNotes').textContent = u.notes || '（这个版本没写更新说明）';
      $('#updNotes').hidden = false;
    } else {
      $('#updLatest').textContent = '已是最新';
      $('#updApply').hidden = true;
      $('#updNotes').hidden = true;
    }
  }catch(err){ toast(err.message, true); }
  e.target.disabled = false;
  e.target.textContent = '检查更新';
};

// 一键更新：后端下载替换二进制并重启服务，进程重启期间界面会短暂断连
$('#updApply').onclick = async e => {
  if(!confirm('更新到 ' + $('#updApplyVer').textContent + '？服务会重启，界面会短暂断开。')) return;
  e.target.disabled = true;
  e.target.textContent = '更新中…';
  try{
    const r = await api('/api/update/apply', {method:'POST'});
    if(r.restarting){
      $('#updNotes').textContent = '已下载新版本，服务正在重启，几秒后刷新页面即可。';
      $('#updNotes').hidden = false;
      toast('更新中，服务重启后刷新页面');
      // 给服务重启留点时间再自动刷新
      setTimeout(() => location.reload(), 6000);
    } else {
      toast(r.message || '已是最新版');
      e.target.disabled = false;
      e.target.textContent = '更新到 ' + $('#updApplyVer').textContent;
    }
  }catch(err){
    toast(err.message, true);
    e.target.disabled = false;
    e.target.textContent = '更新到 ' + $('#updApplyVer').textContent;
  }
};

// 端口/监听地址变了要提示用户之后从新地址进；密码/路径可原地生效
function nextURL(port, listen, path){
  const host = (listen && listen !== '0.0.0.0') ? listen : location.hostname;
  return location.protocol + '//' + host + ':' + port + (path ? '/' + path : '') + '/';
}

$('#setSave').onclick = async e => {
  e.target.disabled = true;
  const body = {};
  const pw = $('#setPw').value.trim();
  if(pw) body.password = pw;
  body.base_path = $('#setPath').value.trim();
  const port = parseInt($('#setPort').value.trim(), 10);
  if(port) body.port = port;
  body.listen_addr = $('#setListen').value;
  body.residential_only = $('#setResi').checked;

  const portChanged = curSettings && (port !== curSettings.port
    || body.listen_addr !== (curSettings.listen_addr || '0.0.0.0'));

  try{
    await api('/api/settings', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify(body),
    });
    if(portChanged){
      const url = nextURL(port, body.listen_addr, body.base_path);
      $('#setPortHint').innerHTML = '监听已切换，请从新地址打开：<a href="' + esc(url) + '">' + esc(url) + '</a>';
      toast('监听已切换，用新地址重新打开');
      // 端口变了当前连接会断，不自动跳转，让用户看清新地址
    } else {
      toast('已保存');
      // 路径可能变了，重新加载到新路径下
      const np = body.base_path;
      const cur = (curSettings && curSettings.base_path || '').replace(/^\//, '');
      if(np !== cur){
        location.href = location.protocol + '//' + location.host
          + (np ? '/' + np : '') + '/';
        return;
      }
      closeModal('settings');
      poll();
    }
  }catch(err){ toast(err.message, true); }
  e.target.disabled = false;
};

poll();
setInterval(poll, 3000);
