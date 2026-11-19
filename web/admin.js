/* 网页面板的新标签页：节点入站 / 用户管理 / Nginx 与证书 / 系统。
   依赖 app.js 里的 $ / api / esc / toast / copy / fmtBytes。 */

// ---- 小工具 ----

async function postJSON(path, body){
  // 界面挂在随机前缀下，请求必须用相对路径（与 app.js 的 api() 一致）
  const r = await fetch(path.replace(/^\//, ''), {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body || {})
  });
  if(r.status === 401){ location.reload(); return {error: '登录已过期'}; }
  return await r.json().catch(() => ({}));
}

// 一次性 Cloudflare 凭证：从 prefix 开头的几个输入框读出来，随请求发出去，后端用完即删、不保存。
function cfOnceBody(prefix){
  const v = id => { const el = document.getElementById(prefix + id); return el ? el.value.trim() : ''; };
  const out = {};
  if(v('Tok')) out.cf_token = v('Tok');
  if(v('Mail') || v('Key')){ out.cf_email = v('Mail'); out.cf_key = v('Key'); }
  return out;
}
function cfOnceHTML(prefix){
  return '<details class="cfonce" id="' + prefix + 'Once"><summary>临时使用 Cloudflare 凭证（用完即删，不保存）</summary>'
    + '<label class="field"><span>API Token</span><input id="' + prefix + 'Tok" type="password" autocomplete="off" spellcheck="false" placeholder="留空则用已保存的凭证"></label>'
    + '<div class="row2"><label class="field"><span>或：登录邮箱</span><input id="' + prefix + 'Mail" type="text" autocomplete="off" spellcheck="false"></label>'
    + '<label class="field"><span>Global API Key</span><input id="' + prefix + 'Key" type="password" autocomplete="off" spellcheck="false"></label></div>'
    + '<button type="button" id="' + prefix + 'Load">用这个凭证读取域名</button></details>';
}
// 用一次性凭证读域名，填进给定的下拉框
async function cfOnceLoadZones(prefix, selects){
  const body = cfOnceBody(prefix);
  if(!body.cf_token && !body.cf_email) return toast('先填 Token 或 邮箱 + Global Key', true);
  const d = await postJSON('/api/cf/zones', body);
  if(d.error) return toast(d.error, true);
  const opts = (d.zones || []).map(z => '<option>' + esc(z.name || z) + '</option>').join('')
    || '<option value="">这个凭证下没有域名</option>';
  selects.forEach(sel => { const el = $(sel); if(el) el.innerHTML = opts; });
  toast('已读取 ' + (d.zones || []).length + ' 个域名');
}
function clearCFOnce(prefix){
  ['Tok', 'Mail', 'Key'].forEach(k => { const el = document.getElementById(prefix + k); if(el) el.value = ''; });
}

// 用户限速 / 流量摘要（数据来自 app.js 的 users）
function userLimitOf(name){
  const list = (typeof users !== 'undefined' && users && users.users) || [];
  return list.find(x => x.name === name);
}
function userLimitText(name){
  const u = userLimitOf(name);
  if(!u) return '';
  const sp = u.speed ? ('限速 ' + u.speed.text) : '不限速';
  const tr = u.traffic && u.traffic.enabled
    ? ('流量 ' + fmtBytes(u.period_total) + ' / ' + fmtBytes(u.traffic.limit_bytes))
    : ('已用 ' + fmtBytes(u.total) + ' · 不限量');
  return sp + ' · ' + tr + (u.traffic && u.traffic.disabled_by_limit ? ' · 已到量停用' : '');
}

function tag(cls, text){ return '<span class="badge ' + cls + '">' + esc(text) + '</span>'; }
function onoff(v, yes, no){ return v ? tag('ok', yes || '已开启') : tag('', no || '未开启'); }
function measured(n){
  if(n === undefined || n === null) return '—';
  return fmtBytes(n);
}
function shortDate(s){ return (s || '').slice(0, 16).replace('T', ' '); }

function confirmBox(text){
  return window.confirm(text);
}

// ---- 标签页 ----

const PANES = ['tunnels', 'nodes', 'subs', 'rules', 'infra', 'system'];
let curTab = '';

function switchTab(name, push){
  if(!PANES.includes(name)) name = 'tunnels';
  curTab = name;
  document.querySelectorAll('.tabs button').forEach(b => {
    b.classList.toggle('on', b.dataset.tab === name);
  });
  document.querySelectorAll('.pane').forEach(p => {
    p.classList.toggle('on', p.id === 'pane-' + name);
  });
  try{ localStorage.setItem('fanout-tab', name); }catch(e){}
  if(push !== false){ window.scrollTo({top:0, behavior:'instant'}); }
  loadPane(name);
}

document.addEventListener('click', e => {
  const t = e.target.closest('.tabs button');
  if(t) switchTab(t.dataset.tab);
});

function loadPane(name){
  if(name === 'nodes') loadNodes();
  if(name === 'subs') loadSubs();
  if(name === 'infra') loadInfra();
  if(name === 'system') loadSystem();
}

// 轮询里顺手刷新当前标签页，但别把用户正在填的表单冲掉
let tick = 0;
function onPollTick(){
  tick++;
  const modalOpen = document.querySelector('.modal.open');
  if(modalOpen) return;
  // poll 每 3 秒一次；节点要读 nft 和证书目录，别跟着每 3 秒跑一遍
  if(curTab === 'nodes' && tick % 4 === 0) loadNodes(true);
  if(curTab === 'subs' && tick % 3 === 0) loadSubs(true);
  if(curTab === 'system' && tick % 5 === 0) loadSystem(true);
  if(curTab === 'tunnels' && tick % 5 === 0) loadNodes(true);
}

// ================= 节点入站 =================

var nodesState = {nodes: [], kinds: [], reality_sites: [], cert_domains: [], quick: [], public_ip: ''};
var nodeCur = null;

async function loadNodes(quiet){
  try{
    const d = await api('/api/nodes');
    if(d.error){ if(!quiet) toast(d.error, true); return; }
    nodesState = d;
    renderNodes();
    if(nodeCur && $('#nodesheet').classList.contains('open')){
      // 面板开着时只更新标题上的状态，别重建表单
      const n = (d.nodes || []).find(x => x.file === nodeCur.file);
      if(n) nodeCur = Object.assign(nodeCur, {quick_on: n.quick_on, quick_domain: n.quick_domain,
        port_ok: n.port_ok, cdn_domains: n.cdn_domains, links: n.links, hop_on: n.hop_on, obfs_on: n.obfs_on});
    }
  }catch(e){ if(!quiet) toast('读取节点失败', true); }
}

function nodeBadges(n){
  const out = [];
  const okT = n.port_ok && n.port_ok.tcp;
  const okU = n.port_ok && n.port_ok.udp;
  const need = (n.kind === 'hysteria2' || n.kind === 'tuic') ? 'udp'
    : (n.kind === 'xhttp-udp-tls' || n.kind === 'naiveproxy') ? 'both' : 'tcp';
  if(need === 'both') out.push((okT && okU) ? tag('ok', 'TCP/UDP 已放行') : tag('bad', '端口未放行'));
  else if(need === 'udp') out.push(okU ? tag('ok', 'UDP 已放行') : tag('bad', 'UDP 未放行'));
  else out.push(okT ? tag('ok', 'TCP 已放行') : tag('bad', '端口未放行'));
  if(n.quick) out.push(tag(n.quick_on ? 'ok' : '', n.quick_on ? '隧道运行中' : '隧道已停止'));
  if((n.cdn_domains || []).length) out.push(tag('acc', 'CDN'));
  if(n.hop_on) out.push(tag('acc', '端口跳跃'));
  if(n.obfs_on) out.push(tag('acc', 'Gecko 混淆'));
  if(n.tls && !n.reality) out.push(tag('', 'TLS'));
  if(n.reality) out.push(tag('warn', 'Reality'));
  return out.join('');
}

function renderNodes(){
  const box = $('#nodes');
  const all = nodesState.nodes || [];
  // CF 临时隧道是专用入站，单独一栏，不和普通节点混在一起
  const list = all.filter(n => !n.quick);
  const quick = all.filter(n => n.quick);
  $('#ncount').textContent = list.length + ' 个';
  $('#qcount').textContent = quick.length + ' 个';
  $('#quicknodes').innerHTML = quick.map(nodeCardHTML).join('')
    || '<div class="empty small">还没有临时隧道。</div>';
  if(!list.length){
    box.innerHTML = '<div class="empty small">还没有入站。点右上角「添加入站」建一个。</div>';
    return;
  }
  box.innerHTML = list.map(nodeCardHTML).join('');
}

function nodeCardHTML(n){
  {
    const users = (n.users || []).length;
    const quick = n.quick ? (' · 隧道域名 ' + (n.quick_domain || '未分配')) : '';
    return '<div class="card" data-file="' + esc(n.file) + '">'
      + '<div class="top"><span class="name">' + esc(n.tag) + '</span>'
      + '<span class="badge">' + esc(n.protocol) + '</span>'
      + '<span class="badge acc">:' + n.port + '</span></div>'
      + '<div class="sub">' + (n.remark ? esc(n.remark) + ' · ' : '') + users + ' 个用户' + esc(quick) + '</div>'
      + '<div class="top" style="margin-top:7px;gap:5px">' + nodeBadges(n) + '</div>'
      + '<div class="acts">'
      + '<button data-nact="manage">管理</button>'
      + '<button data-nact="links">链接</button>'
      + '<button data-nact="config">配置</button>'
      + '<span class="spacer"></span>'
      + '<button class="danger" data-nact="delete">删除</button>'
      + '</div></div>';
  }
}

document.addEventListener('click', async e => {
  const btn = e.target.closest('[data-nact]');
  if(!btn) return;
  const card = btn.closest('[data-file]');
  if(!card) return;
  const file = card.dataset.file;
  const act = btn.dataset.nact;
  if(act === 'manage') return openNodeSheet(file);
  if(act === 'links') return showNodeLinks(file);
  if(act === 'config') return showNodeConfig(file);
  if(act === 'delete') return deleteNodeUI(file);
});

async function showNodeLinks(file){
  const d = await api('/api/nodes/detail?file=' + encodeURIComponent(file));
  if(d.error) return toast(d.error, true);
  $('#logtitle').textContent = '分享链接 · ' + (d.node && d.node.tag || file);
  $('#logtext').value = ((d.node && d.node.links) || []).join('\n') || '（还没有链接文件）';
  $('#logcopy').onclick = () => copy($('#logtext').value);
  openModal('logbox');
}

async function showNodeConfig(file){
  const d = await api('/api/nodes/config?file=' + encodeURIComponent(file));
  if(d.error) return toast(d.error, true);
  $('#logtitle').textContent = '入站配置 · ' + file;
  $('#logtext').value = d.config || '';
  $('#logcopy').onclick = () => copy($('#logtext').value);
  openModal('logbox');
}

async function deleteNodeUI(file){
  const n = (nodesState.nodes || []).find(x => x.file === file);
  const hasCDN = n && (n.cdn_domains || []).length;
  const msg = '确定删除 ' + file + '？\n配置文件、链接、端口放行会一起清掉'
    + (n && n.quick ? '，临时隧道服务也会停掉' : '')
    + (hasCDN ? '。\n该入站有 CDN（' + n.cdn_domains.join('、') + '），确定后还会问要不要删 DNS。' : '。');
  if(!confirmBox(msg)) return;
  let removeCDN = false;
  if(hasCDN) removeCDN = confirmBox('同时删除 Cloudflare 上的回源规则和 DNS 记录？');
  const d = await postJSON('/api/nodes/delete', {file: file, remove_cdn: removeCDN});
  if(d.error) return toast(d.error, true);
  toast('已删除 ' + file);
  loadNodes();
}

// ---- 节点详情面板 ----

async function ensureZones(){
  if((infraState.zones || []).length) return;
  try{
    const cf = await api('/api/cf');
    infraState.cf = cf;
    infraState.zones = cf.zones || [];
  }catch(e){}
}

async function openNodeSheet(file){
  await ensureZones();
  if(typeof users !== 'undefined' && !(users && (users.users || []).length)){
    try{ users = await api('/api/users'); }catch(e){}
  }
  const d = await api('/api/nodes/detail?file=' + encodeURIComponent(file));
  if(d.error) return toast(d.error, true);
  nodeCur = d.node;
  nodeCur.obfs_password = d.obfs_password || '';
  $('#nodetitle').textContent = nodeCur.tag;
  $('#nodebody').innerHTML = nodeSheetHTML(nodeCur, d.config || '');
  bindNodeSheet(nodeCur);
  openModal('nodesheet');
}

// 临时隧道协议切换：只给 WebSocket 目标。CF 会缓冲 XHTTP 的流式响应，
// 切过去就不通了（脚本 931981a 也是这个结论），老的 XHTTP 入站可以切回 WS。
function switchButtons(kind){
  const btn = (t, label) => '<button data-qswitch="' + t + '">' + label + '</button>';
  if(kind === 'vless-ws') return btn('vmess-ws', '切到 VMess WS');
  if(kind === 'vmess-ws') return btn('vless-ws', '切到 VLESS WS');
  if(kind === 'vless-xhttp') return btn('vless-ws', '切回 VLESS WS') + btn('vmess-ws', '切回 VMess WS');
  return '';
}

function nodeSheetHTML(n, config){
  const users = (n.users || []).map((u, i) => {
    const cred = u.uuid || u.username || u.password || '';
    const lim = u.name ? userLimitText(u.name) : '';
    return '<div class="drow"><span class="grow"><b>' + esc(u.name || ('用户' + (i + 1))) + '</b>'
      + '<div class="dim mono">' + esc(cred) + '</div>'
      + (lim ? '<div class="ulim">' + esc(lim) + '</div>' : '') + '</span>'
      + (u.name ? '<button data-ulimit="' + esc(u.name) + '">限速/流量</button>' : '')
      + '<button data-cred="' + esc(u.name) + '">换 UUID</button></div>';
  }).join('');
  const tcp = n.port_ok && n.port_ok.tcp, udp = n.port_ok && n.port_ok.udp;
  const isReality = n.reality;
  const isHy2 = n.kind === 'hysteria2';
  // 临时隧道专用入站只显示隧道相关；普通节点不出现临时隧道
  const canCDN = !n.quick && ['vless-ws', 'vmess-ws', 'trojan-ws', 'vless-xhttp', 'xhttp-udp-tls'].includes(n.kind);
  const canTunnel = canCDN;
  const canQuick = !!n.quick;
  const cdn = (n.cdn_domains || []).map(d =>
    '<div class="drow"><span class="grow">' + esc(d) + '</span>'
    + '<button class="danger" data-delcdn="' + esc(d) + '">删除</button></div>').join('');

  return ''
    + '<div class="top" style="gap:5px;margin-bottom:9px">' + nodeBadges(n) + '</div>'

    + '<div class="sec" style="border-top:0;margin-top:0;padding-top:0"><h3>基本信息</h3>'
    + '<div class="kvgrid">'
    + '<div class="kvitem"><b>' + n.port + '</b><span>监听端口（' + (n.listen || '::') + '）</span></div>'
    + '<div class="kvitem"><b>' + (n.users || []).length + '</b><span>用户</span></div>'
    + '<div class="kvitem"><b>' + esc(n.protocol) + '</b><span>协议</span></div>'
    + '<div class="kvitem"><b>' + esc(n.file) + '</b><span>配置文件</span></div>'
    + '</div></div>'

    + '<div class="sec"><h3>用户<button data-credall="1" style="margin-left:auto">全部换新</button></h3>'
    + (users || '<div class="hintline">这个入站还没有用户</div>') + '</div>'

    + (n.quick ? '' : ('<div class="sec"><h3>修改端口</h3>'
    + '<div class="rowbtn"><input id="nPort" type="text" inputmode="numeric" value="' + n.port + '" style="flex:1;min-width:110px">'
    + '<button class="primary" id="nPortSave">保存</button></div></div>'))

    + ((canCDN || canTunnel) ? '<div class="sec"><h3>Cloudflare 凭证</h3>' + cfOnceHTML('nCF') + '</div>' : '')

    + (isReality ? ('<div class="sec"><h3>Reality 伪装域名</h3>'
      + '<div class="rowbtn"><select id="nSNI" style="flex:1;min-width:150px">'
      + (nodesState.reality_sites || []).map(s => '<option' + (n.sni === s ? ' selected' : '') + '>' + esc(s) + '</option>').join('')
      + '</select><button class="primary" id="nSNISave">保存</button></div>'
      + '<div class="rowbtn" style="margin-top:8px"><input id="nSNICustom" type="text" placeholder="或自己填一个域名" style="flex:1;min-width:140px">'
      + '<button id="nSNICustomSave">用这个</button></div></div>') : '')

    + (isHy2 ? ('<div class="sec"><h3>Hysteria2 端口跳跃' + (n.hop_on ? tag('ok', '已开启') : tag('', '未开启')) + '</h3>'
      + '<div class="row2"><label class="field"><span>起始端口</span><input id="nHopMin" type="text" inputmode="numeric" placeholder="20000"></label>'
      + '<label class="field"><span>结束端口</span><input id="nHopMax" type="text" inputmode="numeric" placeholder="20100"></label></div>'
      + '<div class="rowbtn"><button id="nHopOn">开启 / 更新</button>'
      + (n.hop_on ? '<button class="danger" id="nHopOff">关闭</button>' : '') + '</div>'
      + '<div class="hintline">需要服务器放行整个区间；NAT 机注意可用端口范围。</div>'
      + '<h3 style="margin-top:14px">Gecko 混淆' + (n.obfs_on ? tag('ok', '已开启') : tag('', '未开启')) + '</h3>'
      + (n.obfs_on ? '<div class="hintline mono">密码 ' + esc(n.obfs_password || '（未读到）') + '</div>' : '')
      + '<div class="rowbtn" style="margin-top:8px">'
      + '<button id="nObfsOn">' + (n.obfs_on ? '重新生成密码' : '开启') + '</button>'
      + (n.obfs_on ? '<button class="danger" id="nObfsOff">关闭</button>' : '') + '</div></div>') : '')

    + (canCDN ? ('<div class="sec"><h3>CDN</h3>'
      + (cdn || '<div class="hintline">还没有开 CDN</div>')
      + '<div class="row2" style="margin-top:9px"><label class="field"><span>Cloudflare 域名</span><select id="nCDNZone"><option value="">加载中…</option></select></label>'
      + '<label class="field"><span>前缀（留空用根域名）</span><input id="nCDNPrefix" type="text" placeholder="cdn"></label></div>'
      + '<div class="rowbtn"><button class="primary" id="nCDNOn">开启 CDN</button></div></div>') : '')

    + (canTunnel ? ('<div class="sec"><h3>固定隧道（Cloudflare Tunnel）</h3>'
      + '<div class="row2"><label class="field"><span>域名</span><select id="nTunZone"><option value="">加载中…</option></select></label>'
      + '<label class="field"><span>前缀（留空用根域名）</span><input id="nTunPrefix" type="text" placeholder="node"></label></div>'
      + '<div class="rowbtn"><button class="primary" id="nTunOn">开启隧道</button></div></div>') : '')

    + (canQuick ? ('<div class="sec"><h3>临时隧道' + (n.quick_on ? tag('acc', '运行中') : tag('', '未运行')) + '</h3>'
      + '<div class="hintline mono">' + esc(n.quick_domain || '还没有分配域名') + '</div>'
      + '<div class="rowbtn" style="margin-top:9px">'
      + '<button class="primary" id="nQuickStart">' + (n.quick_on ? '重启（换域名）' : '启动') + '</button>'
      + (n.quick_on ? '<button class="danger" id="nQuickStop">停止</button>' : '')
      + '<button id="nQuickCreds">重置 UUID/路径</button>'
      + switchButtons(n.kind)
      + '</div></div>') : '')

    + '<div class="sec"><h3>链接</h3><div class="linkbox" id="nLinks">'
    + ((n.links || []).map(l => esc(l)).join('\n') || '（还没有链接文件）') + '</div>'
    + '<div class="rowbtn" style="margin-top:8px"><button id="nCopyLinks">复制全部</button>'
    + '<button id="nShowConfig">查看配置</button></div></div>'

    + '<div class="sec"><h3>危险操作</h3><div class="rowbtn">'
    + '<button class="danger" id="nDelete">删除这个入站</button></div></div>';
}

function bindNodeSheet(n){
  const file = n.file;
  const run = async (path, body, okText) => {
    const d = await postJSON(path, body);
    if(d.error) return toast(d.error, true);
    toast(okText);
    const fresh = await api('/api/nodes/detail?file=' + encodeURIComponent(file));
    if(!fresh.error){
      nodeCur = fresh.node;
      nodeCur.obfs_password = fresh.obfs_password || '';
      $('#nodebody').innerHTML = nodeSheetHTML(nodeCur, fresh.config || '');
      bindNodeSheet(nodeCur);
    }
    loadNodes(true);
  };

  document.querySelectorAll('#nodebody [data-cred]').forEach(b => {
    b.onclick = () => {
      if(!confirmBox('把 ' + b.dataset.cred + ' 的 UUID/密码换成新的？旧链接会失效，订阅会自动更新。')) return;
      run('/api/nodes/cred', {file: file, user: b.dataset.cred}, '已换新凭据');
    };
  });
  const all = document.querySelector('#nodebody [data-credall]');
  if(all) all.onclick = () => {
    if(!confirmBox('把这个入站所有用户的凭据都换一遍？')) return;
    (n.users || []).reduce((p, u) => p.then(() =>
      postJSON('/api/nodes/cred', {file: file, user: u.name})), Promise.resolve())
      .then(() => { toast('凭据已全部换新'); openNodeSheet(file); loadNodes(true); });
  };
  $('#nPortSave').onclick = () => {
    const p = Number($('#nPort').value);
    if(!p) return toast('端口要填数字', true);
    run('/api/nodes/port', {file: file, port: p}, '端口已改');
  };
  if($('#nSNISave')) $('#nSNISave').onclick = () => run('/api/nodes/reality', {file, sni: $('#nSNI').value}, 'Reality 域名已改');
  if($('#nSNICustomSave')) $('#nSNICustomSave').onclick = () => {
    const v = $('#nSNICustom').value.trim();
    if(!v) return toast('先填域名', true);
    run('/api/nodes/reality', {file, sni: v}, 'Reality 域名已改');
  };
  if($('#nHopOn')) $('#nHopOn').onclick = () => {
    const min = Number($('#nHopMin').value), max = Number($('#nHopMax').value);
    if(!min || !max) return toast('起始和结束端口都要填', true);
    run('/api/nodes/hy2', {file, action: 'hop_on', min_port: min, max_port: max}, '端口跳跃已开启');
  };
  if($('#nHopOff')) $('#nHopOff').onclick = () => {
    if(!confirmBox('关闭端口跳跃？')) return;
    run('/api/nodes/hy2', {file, action: 'hop_off'}, '端口跳跃已关闭');
  };
  if($('#nObfsOn')) $('#nObfsOn').onclick = () => run('/api/nodes/hy2', {file, action: 'obfs_on'}, 'Gecko 混淆已开启');
  if($('#nObfsOff')) $('#nObfsOff').onclick = () => {
    if(!confirmBox('关闭 Gecko 混淆？')) return;
    run('/api/nodes/hy2', {file, action: 'obfs_off'}, 'Gecko 混淆已关闭');
  };
  document.querySelectorAll('#nodebody [data-delcdn]').forEach(b => {
    b.onclick = () => {
      const dom = b.dataset.delcdn;
      if(!confirmBox('删掉 CDN ' + dom + '？会同时删 Cloudflare 的回源规则和 DNS。')) return;
      run('/api/nodes/cdn', Object.assign({file, action: 'disable', domain: dom}, cfOnceBody('nCF')), 'CDN 已删除');
    };
  });
  if($('#nCFLoad')) $('#nCFLoad').onclick = () => cfOnceLoadZones('nCF', ['#nCDNZone', '#nTunZone']);
  document.querySelectorAll('#nodebody [data-ulimit]').forEach(b => {
    b.onclick = e => { e.stopPropagation(); openUser(b.dataset.ulimit, () => openNodeSheet(file)); };
  });
  if($('#nCDNOn')){
    fillZoneSelect('#nCDNZone');
    $('#nCDNOn').onclick = async () => {
      const zone = $('#nCDNZone').value;
      if(!zone) return toast('先选 Cloudflare 域名', true);
      const d = await postJSON('/api/nodes/cdn', Object.assign({file, action: 'enable', zone, prefix: $('#nCDNPrefix').value.trim()}, cfOnceBody('nCF')));
      clearCFOnce('nCF');
      if(d.error) return toast(d.error, true);
      toast('CDN 已开：' + (d.domain || ''));
      if(d.warning) setTimeout(() => toast(d.warning, true), 1200);
      openNodeSheet(file); loadNodes(true);
    };
  }
  if($('#nTunOn')){
    fillZoneSelect('#nTunZone');
    $('#nTunOn').onclick = async () => {
      const zone = $('#nTunZone').value;
      if(!zone) return toast('先选 Cloudflare 域名', true);
      const d = await postJSON('/api/nodes/tunnel', Object.assign({file, zone, prefix: $('#nTunPrefix').value.trim()}, cfOnceBody('nCF')));
      clearCFOnce('nCF');
      if(d.error) return toast(d.error, true);
      toast('隧道已开：' + (d.domain || ''));
      openNodeSheet(file); loadNodes(true);
    };
  }
  const quickSwitch = async (target) => {
    const d = await postJSON('/api/quick/action', {file, action: 'switch', target});
    if(d.error) return toast(d.error, true);
    toast('已切到 ' + target);
    openNodeSheet(d.node ? d.node.file : file); loadNodes(true);
  };
  const quickCall = async (action, okText) => {
    const d = await postJSON('/api/quick/action', {file, action});
    if(d.error) return toast(d.error, true);
    toast(okText);
    openNodeSheet(file); loadNodes(true);
  };
  if($('#nQuickStart')) $('#nQuickStart').onclick = () => quickCall('start', '临时隧道已启动');
  if($('#nQuickStop')) $('#nQuickStop').onclick = () => {
    if(!confirmBox('停止临时隧道？进程会退出，链接从订阅里拿掉，数据保留。')) return;
    quickCall('stop', '临时隧道已停止');
  };
  if($('#nQuickCreds')) $('#nQuickCreds').onclick = () => {
    if(!confirmBox('重置 UUID 和 WS 路径？已经连上的客户端要重新导入。')) return;
    quickCall('reset_creds', '凭据已重置');
  };
  document.querySelectorAll('#nodebody [data-qswitch]').forEach(b => {
    b.onclick = () => {
      const target = b.dataset.qswitch;
      if(!confirmBox('把临时隧道切到 ' + target + '？\n会换一个配置文件（编号 +1），端口、路径和用户不变。'
        + '\n（CF 临时隧道只支持 WebSocket，XHTTP 会不通）')) return;
      quickSwitch(target);
    };
  });
  $('#nCopyLinks').onclick = () => copy($('#nLinks').textContent);
  $('#nShowConfig').onclick = () => {
    $('#logtitle').textContent = '入站配置 · ' + file;
    api('/api/nodes/config?file=' + encodeURIComponent(file)).then(d => {
      if(d.error) return toast(d.error, true);
      $('#logtext').value = d.config || '';
      $('#logcopy').onclick = () => copy($('#logtext').value);
      openModal('logbox');
    });
  };
  $('#nDelete').onclick = () => { closeModal('nodesheet'); deleteNodeUI(file); };
}

// ---- 添加入站 ----

function openNodeCreate(){
  const kinds = (nodesState.kinds || []).filter(k => k.kind !== 'cf-quick');
  $('#ncKind').innerHTML = kinds.map(k => '<option value="' + k.kind + '">' + esc(k.label) + ' · ' + esc(k.note) + '</option>').join('');
  $('#ncSNI').innerHTML = (nodesState.reality_sites || []).map(s => '<option>' + esc(s) + '</option>').join('');
  $('#ncSNI').value = (nodesState.reality_sites || []).slice(-1)[0] || 'www.iij.ad.jp';
  fillCertSelect('#ncDomain');
  updateKindFields();
  openModal('nodecreate');
}

function updateKindFields(){
  const kind = $('#ncKind').value;
  const k = (nodesState.kinds || []).find(x => x.kind === kind) || {};
  const tls = k.tls || 'none';
  $('#ncTLSWrap').hidden = !(tls === 'domain' || tls === 'optional');
  $('#ncDomainWrap').hidden = !(tls === 'domain');
  $('#ncAddTLSWrap').hidden = (tls !== 'optional');
  $('#ncSNIWrap').hidden = (tls !== 'reality');
  $('#ncHint').textContent = k.note || '';
}

function fillCertSelect(sel){
  const el = $(sel);
  if(!el) return;
  const list = nodesState.cert_domains || [];
  el.innerHTML = list.length
    ? list.map(d => '<option>' + esc(d) + '</option>').join('')
    : '<option value="">（还没有域名证书，先去「Nginx与证书」申请）</option>';
}

function openSubAdd(){
  $('#saName').value = '';
  $('#saUUID').value = '';
  const nodes = (nodesState.nodes || []);
  $('#saFiles').innerHTML = nodes.length
    ? nodes.map(n => '<label><input type="checkbox" value="' + esc(n.file) + '" checked> '
      + esc(n.tag) + ' <span class="dim">:' + n.port + '</span></label>').join('')
    : '<div class="hintline">还没有入站，先添加一个</div>';
  openModal('subadd');
}

// ================= 用户管理 =================

var subsState = {subs: [], nodes: []};

async function loadSubs(quiet){
  try{
    const d = await api('/api/subs');
    if(d.error){ if(!quiet) toast(d.error, true); return; }
    subsState = d;
    renderSubs();
  }catch(e){ if(!quiet) toast('读取用户失败', true); }
}

function renderSubs(){
  const box = $('#subs');
  const list = subsState.subs || [];
  $('#scount').textContent = list.length + ' 个';
  if(!list.length){
    box.innerHTML = '<div class="empty small">还没有订阅用户。</div>';
    return;
  }
  box.innerHTML = list.map(s => {
    const chips = (s.protocols || []).map(p => '<span class="badge">' + esc(p) + '</span>').join('');
    const traf = s.limit_on && s.limit_bytes
      ? ('已用 ' + fmtBytes(s.traffic_used) + ' / ' + fmtBytes(s.limit_bytes))
      : ('已用 ' + fmtBytes(s.traffic_used) + ' · 无限制');
    return '<div class="card" data-sub="' + esc(s.name) + '">'
      + '<div class="top"><span class="name">' + esc(s.name) + '</span>'
      + (s.speed_text ? '<span class="badge warn">' + esc(s.speed_text) + '</span>' : '') + '</div>'
      + '<div class="sub">' + esc(traf) + ' · ' + (s.links || []).length + ' 条节点</div>'
      + '<div class="top" style="margin-top:6px;gap:5px">' + (chips || tag('bad', '没有挂任何入站')) + '</div>'
      + '<div class="sub mono">' + esc(s.sub_url || '（订阅地址未生成）') + '</div>'
      + (userLimitText(s.name) ? '<div class="ulim">' + esc(userLimitText(s.name)) + '</div>' : '')
      + '<div class="acts"><button data-sact="copy">复制订阅</button>'
      + '<button data-ulimit="' + esc(s.name) + '">限速/流量</button>'
      + '<button data-sact="links">看节点</button>'
      + '<button data-sact="rebuild">重建</button>'
      + '<span class="spacer"></span>'
      + '<button class="danger" data-sact="delete">删除</button></div></div>';
  }).join('');
}

document.addEventListener('click', async e => {
  const btn = e.target.closest('[data-sact]');
  if(!btn) return;
  const card = btn.closest('[data-sub]');
  if(!card) return;
  const name = card.dataset.sub;
  const sub = (subsState.subs || []).find(x => x.name === name);
  const act = btn.dataset.sact;
  if(act === 'copy'){
    if(!sub || !sub.sub_url) return toast('没有订阅地址', true);
    return copy(sub.sub_url);
  }
  if(act === 'links'){
    $('#logtitle').textContent = '节点链接 · ' + name;
    $('#logtext').value = ((sub && sub.links) || []).join('\n');
    $('#logcopy').onclick = () => copy($('#logtext').value);
    return openModal('logbox');
  }
  if(act === 'rebuild'){
    if(!confirmBox('重新把 ' + name + ' 挂回全部入站并重建订阅？UUID 不变，已导入的客户端不用改。')) return;
    const d = await postJSON('/api/subs/rebuild', {name});
    if(d.error) return toast(d.error, true);
    toast('已重建'); loadSubs();
    return;
  }
  if(act === 'delete'){
    if(!confirmBox('删除用户 ' + name + '？\n会从所有入站里移除，订阅目录和 Nginx 片段一起清掉（挪进回收目录）。')) return;
    const d = await postJSON('/api/subs/delete', {name});
    if(d.error) return toast(d.error, true);
    toast('已删除'); loadSubs(); loadNodes(true);
  }
});

async function submitSubAdd(){
  const files = Array.from(document.querySelectorAll('#saFiles input:checked')).map(i => i.value);
  const d = await postJSON('/api/subs/add', {
    name: $('#saName').value.trim(),
    uuid: $('#saUUID').value.trim(),
    files
  });
  if(d.error) return toast(d.error, true);
  closeModal('subadd');
  toast('用户已创建');
  loadSubs(); loadNodes(true);
}

// ================= Nginx 与证书 =================

var infraState = {status: {}, sub_domain: '', sub_port: '', sites: [], certs: [], zones: [], cf: {}};

async function loadInfra(){
  const [st, sites, certs, cf] = await Promise.all([
    api('/api/nginx/status').catch(() => ({})),
    api('/api/nginx/sites').catch(() => ({})),
    api('/api/certs').catch(() => ({})),
    api('/api/cf').catch(() => ({}))
  ]);
  infraState.status = st.status || {};
  infraState.sub_domain = st.sub_domain || '';
  infraState.sub_port = st.sub_port || '';
  infraState.sites = sites.sites || [];
  infraState.certs = certs.certs || [];
  infraState.zones = certs.zones || cf.zones || [];
  infraState.cf = cf;
  renderNginx();
  renderCerts();
  renderCF();
}

function renderNginx(){
  const st = infraState.status || {};
  const box = $('#ngxstatus');
  const badges = [];
  badges.push(st.installed ? tag('ok', '已安装') : tag('bad', '未安装'));
  if(st.installed) badges.push(st.active ? tag('ok', '运行中') : tag('', '已停止'));
  badges.push(st.test_ok ? tag('ok', 'nginx -t 通过') : tag('bad', '配置有错'));
  if(st.version) badges.push(tag('', st.version));
  box.innerHTML = '<div class="card"><div class="top"><span class="name">Nginx</span>'
    + badges.join('') + '</div>'
    + (infraState.sub_domain
      ? ('<div class="sub">订阅主站 ' + esc(infraState.sub_domain) + (infraState.sub_port ? ':' + esc(infraState.sub_port) : '') + '</div>') : '')
    + (st.test_err ? '<div class="errline">' + esc(st.test_err) + '</div>' : '')
    + '</div>';

  const sites = infraState.sites || [];
  $('#ngxcount').textContent = sites.length + ' 个';
  $('#ngxsites').innerHTML = sites.map(s => {
    const listen = (s.listen || []).join(' ');
    const names = (s.server_names || []).join(' ');
    const target = (s.proxy_pass || [])[0] || '';
    return '<div class="card" data-site="' + esc(s.name) + '" data-usersite="' + (s.user_site ? '1' : '0') + '">'
      + '<div class="top"><span class="name">' + esc(s.name) + '</span>'
      + (s.protected ? tag('warn', '脚本维护') : '')
      + (s.user_site ? tag('', '订阅片段') : '')
      + '</div>'
      + '<div class="sub">' + esc(listen || '—') + (names ? ' · ' + esc(names) : '')
      + (target ? ' → ' + esc(target) : '') + '</div>'
      + '<div class="acts"><button data-gact="edit">编辑</button>'
      + (s.protected ? '' : '<button class="danger" data-gact="del">删除</button>')
      + '</div></div>';
  }).join('') || '<div class="empty small">conf.d 里还没有站点</div>';
}

function renderCerts(){
  const list = infraState.certs || [];
  $('#certcount').textContent = list.length + ' 张';
  $('#certs').innerHTML = list.map(c => {
    const days = c.days;
    const cls = days < 0 ? 'bad' : days < 14 ? 'warn' : 'ok';
    return '<div class="card" data-cert="' + esc(c.domain) + '">'
      + '<div class="top"><span class="name">' + esc(c.domain) + '</span>'
      + tag(cls, days < 0 ? '已过期' : '剩 ' + days + ' 天')
      + (c.self_signed ? tag('warn', '自签') : tag('ok', 'CA 签发')) + '</div>'
      + '<div class="sub">到期 ' + esc(c.not_after || '读取失败') + (c.issuer ? ' · ' + esc(c.issuer) : '') + '</div>'
      + '<div class="sub">' + ((c.used_by || []).length
        ? '被 ' + esc(c.used_by.join('、')) + ' 使用'
        : '暂无入站/站点使用') + '</div>'
      + '<div class="acts"><button data-cact="path">复制路径</button>'
      + '<button class="danger" data-cact="del">删除</button></div></div>';
  }).join('') || '<div class="empty small">还没有证书。点「申请证书」用 Cloudflare DNS 申请，或申请 15 年 Origin CA。</div>';
}

function renderCF(){
  const cf = infraState.cf || {};
  const kind = cf.kind === 'global' ? 'Global API Key' : 'API Token';
  $('#cfbox').innerHTML = '<div class="card"><div class="top"><span class="name">Cloudflare</span>'
    + (cf.configured ? tag(cf.valid === false ? 'bad' : 'ok', cf.valid === false ? '凭证无效' : '已配置')
      : tag('', '未配置')) + '</div>'
    + '<div class="sub">' + (cf.configured ? esc(cf.hint || kind) : '开 CDN、固定隧道、申请证书都需要它') + '</div>'
    + (cf.error ? '<div class="errline">' + esc(cf.error) + '</div>' : '')
    + ((cf.zones || []).length ? '<div class="sub">域名：' + esc((cf.zones || []).map(z => z.name).join('、')) + '</div>' : '')
    + ((cf.tunnels || []).length ? '<div class="sub">Tunnel：' + esc((cf.tunnels || []).map(t => t.name + '(' + (t.status || '?') + ')').join('、')) + '</div>' : '')
    + '</div>';
}

document.addEventListener('click', async e => {
  const btn = e.target.closest('[data-gact],[data-cact]');
  if(!btn) return;
  if(btn.dataset.gact === 'edit'){
    const card = btn.closest('[data-site]');
    return openSiteEditor(card.dataset.site, card.dataset.usersite === '1');
  }
  if(btn.dataset.gact === 'del'){
    const card = btn.closest('[data-site]');
    const name = card.dataset.site;
    if(!confirmBox('删除 ' + name + '？文件会挪进回收目录，nginx -t 不过会自动还原。')) return;
    const d = await postJSON('/api/nginx/delete', {name, user_site: card.dataset.usersite === '1'});
    if(d.error) return toast(d.error, true);
    toast('已删除'); loadInfra();
    return;
  }
  if(btn.dataset.cact === 'path'){
    const card = btn.closest('[data-cert]');
    const c = (infraState.certs || []).find(x => x.domain === card.dataset.cert);
    if(c) copy(c.cert_path + '\n' + c.key_path);
    return;
  }
  if(btn.dataset.cact === 'del'){
    const card = btn.closest('[data-cert]');
    const domain = card.dataset.cert;
    if(!confirmBox('删除证书 ' + domain + '？\n目录会挪进回收目录；被入站/站点用着的会拒绝删除。')) return;
    const delDNS = confirmBox('同时删除 Cloudflare 上这个域名的 DNS 记录？');
    const d = await postJSON('/api/certs/delete', {domain, delete_dns: delDNS});
    if(d.error) return toast(d.error, true);
    toast(d.note || '已删除'); loadInfra(); loadNodes(true);
  }
});

async function openSiteEditor(name, userSite){
  const d = await api('/api/nginx/site?name=' + encodeURIComponent(name) + (userSite ? '&user=1' : ''));
  if(d.error) return toast(d.error, true);
  $('#nstitle').textContent = name;
  $('#nsname').value = name;
  $('#nsuser').value = userSite ? '1' : '0';
  $('#nscontent').value = d.content || '';
  $('#nssave').disabled = !!d.protected;
  $('#nsdel').hidden = !!d.protected;
  $('#nshint').textContent = d.protected
    ? '这个文件由 sing-box-08.sh 维护，网页只读。'
    : '保存前会跑 nginx -t，不通过自动还原。';
  openModal('ngxsite');
}

async function submitSite(){
  const name = $('#nsname').value.trim();
  const d = await postJSON('/api/nginx/save', {
    name, content: $('#nscontent').value, user_site: $('#nsuser').value === '1'
  });
  if(d.error) return toast(d.error, true);
  closeModal('ngxsite');
  toast('已保存并重载 nginx');
  loadInfra();
}

async function openProxySheet(){
  // 域名 / IP 直接从已有证书里选，不用手输
  if(!(infraState.certs || []).length){
    try{ const c = await api('/api/certs'); infraState.certs = c.certs || []; }catch(e){}
  }
  const names = [];
  (infraState.certs || []).forEach(c => { if(c.domain && !names.includes(c.domain)) names.push(c.domain); });
  const ip = nodesState.public_ip || '';
  let opts = names.map(d => '<option value="' + esc(d) + '">' + esc(d) + '</option>').join('');
  if(ip && !names.includes(ip)) opts += '<option value="' + esc(ip) + '" data-nocert="1">' + esc(ip) + '（本机 IP，没有证书只能 HTTP）</option>';
  $('#pxDomain').innerHTML = opts || '<option value="">（还没有证书，先在下面「证书」里申请）</option>';
  $('#pxDomain').onchange = () => {
    const o = $('#pxDomain').selectedOptions[0];
    const nocert = o && o.dataset.nocert === '1';
    $('#pxTLS').checked = !nocert;
    $('#pxTLS').disabled = nocert;
    if(nocert && $('#pxPort').value === '443') $('#pxPort').value = '80';
  };
  $('#pxPort').value = '443';
  $('#pxTarget').value = '127.0.0.1:9393';
  $('#pxTLS').checked = true;
  $('#pxWS').checked = false;
  $('#pxName').value = '';
  $('#pxExtra').value = '';
  $('#pxTLS').disabled = false;
  $('#pxDomain').onchange();
  openModal('pxnew');
}

async function submitProxy(){
  const d = await postJSON('/api/nginx/proxy', {
    name: $('#pxName').value.trim(),
    domain: $('#pxDomain').value,
    listen_port: Number($('#pxPort').value),
    target: $('#pxTarget').value.trim(),
    tls: $('#pxTLS').checked,
    websocket: $('#pxWS').checked,
    extra: $('#pxExtra').value
  });
  if(d.error) return toast(d.error, true);
  closeModal('pxnew');
  toast('站点已创建并生效');
  loadInfra();
}

function openCertIssue(){
  clearCFOnce('ci');
  fillZoneSelect('#ciZone');
  $('#ciMode').value = 'sub';
  $('#ciPrefix').value = '';
  $('#ciLog').value = '';
  $('#ciLog').hidden = true;
  openModal('certnew');
}

async function submitCertIssue(){
  const btn = $('#ciGo');
  btn.disabled = true;
  const old = btn.textContent;
  btn.textContent = '申请中…（可能要 1-2 分钟）';
  const d = await postJSON('/api/certs/issue', Object.assign({
    zone: $('#ciZone').value,
    mode: $('#ciMode').value,
    prefix: $('#ciPrefix').value.trim(),
    wildcard: $('#ciWild').checked
  }, cfOnceBody('ci')));
  clearCFOnce('ci');
  btn.disabled = false;
  btn.textContent = old;
  if(d.error){
    $('#ciLog').value = d.error;
    $('#ciLog').hidden = false;
    return toast('申请失败，看下面的日志', true);
  }
  $('#ciLog').value = d.log || '';
  $('#ciLog').hidden = !d.log;
  toast('证书已签发：' + (d.cert && d.cert.domain || ''));
  loadInfra(); loadNodes(true);
}

function openCFCred(){
  $('#cfKind').value = (infraState.cf && infraState.cf.kind) || 'token';
  $('#cfToken').value = '';
  $('#cfEmail').value = (infraState.cf && infraState.cf.email) || '';
  $('#cfKey').value = '';
  $('#cfHint').textContent = (infraState.cf && infraState.cf.hint) || '还没有配置';
  syncCFKind();
  openModal('cfcred');
}

function syncCFKind(){
  const token = $('#cfKind').value === 'token';
  $('#cfTokenWrap').hidden = !token;
  $('#cfGlobalWrap').hidden = token;
}

async function submitCFCred(){
  const kind = $('#cfKind').value;
  const body = {action: 'save', kind};
  if(kind === 'token') body.token = $('#cfToken').value.trim();
  else { body.email = $('#cfEmail').value.trim(); body.key = $('#cfKey').value.trim(); }
  const d = await postJSON('/api/cf/cred', body);
  if(d.error) return toast(d.error, true);
  closeModal('cfcred');
  toast('凭证已保存');
  loadInfra(); loadNodes(true);
}

function fillZoneSelect(sel){
  const el = $(sel);
  if(!el) return;
  const zones = infraState.zones || [];
  if(!zones.length){
    el.innerHTML = '<option value="">（没有拉到域名，先配置 Cloudflare 凭证）</option>';
    return;
  }
  el.innerHTML = zones.map(z => '<option value="' + esc(z.name) + '">' + esc(z.name) + '</option>').join('');
}

// ================= 系统 =================

var sysState = null;

async function loadSystem(quiet){
  try{
    const d = await api('/api/system');
    if(d.error){ if(!quiet) toast(d.error, true); return; }
    sysState = d;
    renderSystem();
  }catch(e){ if(!quiet) toast('读取系统信息失败', true); }
}

function renderSystem(){
  if(!sysState) return;
  const o = sysState.overview || {};
  $('#sysver').textContent = 'fanout ' + (o.version || '');
  $('#sysinfo').innerHTML = '<div class="kvgrid">'
    + kv('版本', o.version || '—') + kv('sing-box', o.singbox_version || '—')
    + kv('公网 IP', o.public_ip || '—') + kv('主机', o.hostname || '—')
    + kv('入站', o.node_count || 0) + kv('用户', o.user_count || 0)
    + kv('配置文件', o.conf_count || 0) + kv('防火墙', o.firewall === 'drop' ? '开启（drop）' : '放行（accept）')
    + '</div>';

  $('#services').innerHTML = (sysState.services || []).map(s => {
    const init = s.init ? ' · ' + s.init : '';
    const en = s.enabled ? ' · 开机自启' : '';
    return '<div class="card" data-unit="' + esc(s.name) + '">'
      + '<div class="top"><span class="name">' + esc(s.label) + '</span>'
      + (s.active ? tag('ok', '运行中') : tag('', '已停止'))
      + (s.installed ? '' : tag('bad', '没装')) + '</div>'
      + '<div class="sub">' + esc((s.uptime ? '运行 ' + s.uptime : '') + init + en + (s.pid ? ' · PID ' + s.pid : '')) + '</div>'
      + '<div class="acts">'
      + '<button data-svc="start">启动</button><button data-svc="stop">停止</button>'
      + '<button data-svc="restart">重启</button><button data-svc="reload">重载</button>'
      + '<button data-svc="logs">日志</button></div></div>';
  }).join('');

  const fw = sysState.firewall || {};
  $('#fwcount').textContent = (fw.inbounds || []).length + ' 个入站';
  $('#firewall').innerHTML = '<div class="card"><div class="top"><span class="name">入站端口放行</span>'
    + (fw.available ? (fw.policy === 'drop' ? tag('warn', '防火墙已开启') : tag('ok', '防火墙未拦截')) : tag('bad', '没有 nft'))
    + '</div>'
    + '<div class="dlist" style="margin-top:9px">'
    + ((fw.inbounds || []).map(p => {
      const ok = (p.need || []).every(x => x === 'tcp' ? p.tcp : p.udp);
      return '<div class="drow"><span class="grow">' + esc(p.tag) + ' <span class="dim">:' + p.port + ' ' + esc((p.need || []).join('+')) + '</span></span>'
        + (ok ? tag('ok', '已放行') : tag('bad', '未放行')) + '</div>';
    }).join('') || '<div class="hintline">还没有入站</div>')
    + '</div></div>';


  renderOplog(sysState.ops);
}

function kv(label, value){
  return '<div class="kvitem"><b>' + esc(String(value)) + '</b><span>' + esc(label) + '</span></div>';
}

async function loadOplog(){
  const d = await api('/api/system/oplog');
  if(d.error) return;
  renderOplog(d.ops || []);
}

function renderOplog(list){
  if(!list) return;
  $('#oplog').innerHTML = '<div class="oplist">' + (list.map(o =>
    '<div class="op">' + (o.ok ? tag('ok', 'OK') : tag('bad', '失败'))
    + '<span class="t">' + esc(o.time) + '</span><span class="a">' + esc(o.action) + '</span>'
    + '<span class="d">' + esc(o.detail || '') + '</span>'
    + (o.error ? '<span class="e">' + esc(o.error) + '</span>' : '') + '</div>').join('')
    || '<div class="hintline">还没有操作记录</div>') + '</div>';
}

document.addEventListener('click', async e => {
  const btn = e.target.closest('[data-svc]');
  if(!btn) return;
  const card = btn.closest('[data-unit]');
  const unit = card.dataset.unit;
  const act = btn.dataset.svc;
  if(act === 'logs'){
    const d = await api('/api/system/logs?unit=' + encodeURIComponent(unit) + '&lines=160');
    $('#logtitle').textContent = '日志 · ' + unit;
    $('#logtext').value = d.log || d.error || '（空）';
    $('#logcopy').onclick = () => copy($('#logtext').value);
    return openModal('logbox');
  }
  if(act === 'stop' && !confirmBox('停止 ' + unit + '？所有节点会断开。')) return;
  if(act === 'restart' && !confirmBox('重启 ' + unit + '？所有节点会断一下。')) return;
  const d = await postJSON('/api/system/service', {unit, action: act});
  if(d.error) return toast(d.error, true);
  toast(unit + ' ' + act + ' 完成');
  loadSystem();
});

async function syncFirewall(){
  const d = await postJSON('/api/firewall', {action: 'sync'});
  if(d.error) return toast(d.error, true);
  toast('已补齐 ' + (d.count || 0) + ' 条放行');
  loadSystem();
}

// ================= 初始化 =================

function initAdmin(){
  // 顶部标签
  let saved = 'tunnels';
  try{ saved = localStorage.getItem('fanout-tab') || 'tunnels'; }catch(e){}
  if(!PANES.includes(saved)) saved = 'tunnels';
  switchTab(saved, false);
  loadNodes(true);

  $('#nodeNew').onclick = openNodeCreate;
  $('#quickNew').onclick = async () => {
    if(!confirmBox('新建一个 CF 临时隧道？\n会建 127.0.0.1 上的 VMess WS 入站并立刻开 trycloudflare 隧道，域名每次重启都会变。')) return;
    const btn = $('#quickNew');
    btn.disabled = true;
    const d = await postJSON('/api/nodes/create', {kind: 'cf-quick', quick: true});
    btn.disabled = false;
    if(d.error) return toast(d.error, true);
    toast('临时隧道已建好');
    loadNodes();
  };
  $('#ciLoad').onclick = () => cfOnceLoadZones('ci', ['#ciZone']);
  $('#nodeRefresh').onclick = () => loadNodes();
  $('#ncKind').onchange = updateKindFields;
  $('#ncCancel').onclick = () => closeModal('nodecreate');
  $('#ncGo').onclick = async () => {
    const kind = $('#ncKind').value;
    const k = (nodesState.kinds || []).find(x => x.kind === kind) || {};
    const tls = k.tls || 'none';
    const body = {kind, port: Number($('#ncPort').value) || 0,
      sni: $('#ncSNI').value, domain: $('#ncDomain').value};
    if(tls === 'optional') body.add_tls = $('#ncAddTLS').checked;
    const btn = $('#ncGo');
    btn.disabled = true;
    const d = await postJSON('/api/nodes/create', body);
    btn.disabled = false;
    if(d.error) return toast(d.error, true);
    closeModal('nodecreate');
    toast('已添加 ' + (d.node && d.node.tag || kind));
    loadNodes();
  };

  $('#subNew').onclick = openSubAdd;
  $('#saCancel').onclick = () => closeModal('subadd');
  $('#saGo').onclick = submitSubAdd;

  $('#ngxNew').onclick = openProxySheet;
  $('#pxCancel').onclick = () => closeModal('pxnew');
  $('#pxGo').onclick = submitProxy;
  $('#ngxReload').onclick = async () => {
    const d = await postJSON('/api/nginx/service', {action: 'reload'});
    if(d.error) return toast(d.error, true);
    toast('nginx 已重载'); loadInfra();
  };
  $('#ngxTest').onclick = async () => {
    const d = await api('/api/nginx/status');
    const st = (d && d.status) || {};
    toast(st.test_ok ? 'nginx -t 通过' : ('配置有错：' + (st.test_err || '')), !st.test_ok);
  };
  $('#nsCancel').onclick = () => closeModal('ngxsite');
  $('#nssave').onclick = submitSite;
  $('#nsdel').onclick = async () => {
    const name = $('#nsname').value;
    if(!confirmBox('删除 ' + name + '？')) return;
    const d = await postJSON('/api/nginx/delete', {name, user_site: $('#nsuser').value === '1'});
    if(d.error) return toast(d.error, true);
    closeModal('ngxsite'); toast('已删除'); loadInfra();
  };

  $('#certNew').onclick = openCertIssue;
  $('#ciCancel').onclick = () => closeModal('certnew');
  $('#ciGo').onclick = submitCertIssue;
  $('#cfEdit').onclick = openCFCred;
  $('#cfKind').onchange = syncCFKind;
  $('#cfCancel').onclick = () => closeModal('cfcred');
  $('#cfSave').onclick = submitCFCred;
  $('#cfClear').onclick = async () => {
    if(!confirmBox('删除本机保存的 Cloudflare 凭证？脚本那边也会跟着没有。')) return;
    const d = await postJSON('/api/cf/cred', {action: 'delete'});
    if(d.error) return toast(d.error, true);
    closeModal('cfcred'); toast('已删除'); loadInfra();
  };

  $('#sysRefresh').onclick = () => loadSystem();
  $('#fwSync').onclick = syncFirewall;
  $('#opRefresh').onclick = loadOplog;
  $('#logclose').onclick = () => closeModal('logbox');
}

let adminInited = false;
function bootAdmin(){
  if(adminInited) return;
  adminInited = true;
  initAdmin();
}
document.addEventListener('DOMContentLoaded', bootAdmin);
if(document.readyState !== 'loading') bootAdmin();
