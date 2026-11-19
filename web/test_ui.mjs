// 用最小 DOM 桩跑一遍前端渲染，验证新标签页不会抛错、也不漏关键信息。
// 运行：node web/test_ui.mjs
import fs from 'node:fs';
import vm from 'node:vm';

class El {
  constructor(id = '') {
    this.id = id;
    this._html = '';
    this.textContent = '';
    this.value = '';
    this.hidden = false;
    this.disabled = false;
    this.dataset = {};
    this.children = [];
    this._classes = new Set();
    this.classList = {
      add: (c) => this._classes.add(c),
      remove: (c) => this._classes.delete(c),
      toggle: (c, on) => { if (on === undefined) { this._classes.has(c) ? this._classes.delete(c) : this._classes.add(c); } else if (on) this._classes.add(c); else this._classes.delete(c); },
      contains: (c) => this._classes.has(c),
    };
  }
  get innerHTML() { return this._html; }
  set innerHTML(v) { this._html = String(v); }
  set onclick(fn) { this._onclick = fn; }
  get onclick() { return this._onclick; }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  closest() { return null; }
  appendChild(c) { this.children.push(c); }
  addEventListener() {}
}

const els = new Map();
function el(sel) {
  const key = String(sel).replace(/^#/, '');
  if (!els.has(key)) els.set(key, new El(key));
  return els.get(key);
}

const document = {
  readyState: 'complete',
  querySelector: (sel) => el(sel),
  querySelectorAll: () => [],
  addEventListener: () => {},
  createElement: () => new El(),
  activeElement: null,
  body: new El('body'),
};
const store = new Map();
const localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
};
const responses = {};
const fetch = async (url) => ({
  json: async () => (responses[url] || { error: '没有这个接口：' + url }),
});

const win = {
  document, localStorage, fetch,
  confirm: () => true,
  alert: () => {},
  scrollTo: () => {},
  setTimeout: (fn) => { try { fn(); } catch (e) {} return 0; },
  clearTimeout: () => {},
  location: { href: '' },
  navigator: { clipboard: { writeText: async () => {} } },
};
win.window = win;

const ctx = vm.createContext({
  ...win,
  console,
  URL,
  URLSearchParams,
  fetch,
  setTimeout: win.setTimeout,
  clearTimeout: win.clearTimeout,
  setInterval: () => 0,
  clearInterval: () => {},
  requestAnimationFrame: (fn) => 0,
});

function load(path) {
  const code = fs.readFileSync(path, 'utf8');
  vm.runInContext(code, ctx, { filename: path });
}

load('web/app.js');
load('web/admin.js');

function assert(cond, msg) {
  if (!cond) {
    console.error('✗ ' + msg);
    process.exitCode = 1;
  } else {
    console.log('✓ ' + msg);
  }
}

// ---- 节点卡片 ----
ctx.nodesState = {
  nodes: [{
    file: 'vless-reality-1.json', kind: 'vless-reality', number: 1, tag: 'vless-reality-1',
    protocol: 'vless', port: 23456, listen: '::', users: [{ name: 'u1', uuid: 'x' }],
    tls: true, reality: true, sni: 'www.iij.ad.jp', port_ok: { tcp: true, udp: false },
    cdn_domains: [], quick: false, hop_on: false, obfs_on: false, links: ['vless://x@1.2.3.4:1#n'],
  }, {
    file: 'hysteria2-1.json', kind: 'hysteria2', number: 1, tag: 'hysteria2-1',
    protocol: 'hysteria2', port: 23457, users: [], port_ok: { tcp: false, udp: false },
    cdn_domains: ['cdn.example.com'], quick: false,
    hop_on: true, obfs_on: true, links: [],
  }, {
    file: 'vmess-ws-1.json', kind: 'vmess-ws', number: 1, tag: 'vmess-ws-1',
    protocol: 'vmess', port: 8101, listen: '127.0.0.1', users: [{ name: 'u1', uuid: 'y' }], port_ok: { tcp: true, udp: false },
    cdn_domains: [], quick: true, quick_on: true, quick_domain: 'a.trycloudflare.com', links: [],
  }],
  kinds: [{ kind: 'vless-reality', label: 'VLESS Reality', proto: 'vless', tls: 'reality', net: 'tcp', note: '最常用' }],
  reality_sites: ['www.iij.ad.jp'],
  cert_domains: ['node.example.com'],
};
ctx.renderNodes();
const nodeHTML = el('#nodes').innerHTML;
assert(nodeHTML.includes('vless-reality-1'), '节点卡片显示 tag');
assert(nodeHTML.includes('TCP 已放行'), '节点卡片显示端口放行状态');
assert(nodeHTML.includes('UDP 未放行'), 'hy2 未放行时标红');
assert(el('#quicknodes').innerHTML.includes('隧道运行中'), '临时隧道标记');
assert(nodeHTML.includes('CDN'), 'CDN 标记');
assert(el('#ncount').textContent.includes('2'), '节点数量统计（临时隧道不计入普通节点）');
const qSheet = ctx.nodeSheetHTML(ctx.nodesState.nodes[2], '{}');
assert(qSheet.includes('临时隧道') && !qSheet.includes('修改端口') && !qSheet.includes('开启 CDN'), '临时隧道面板只放隧道相关');
const rSheet = ctx.nodeSheetHTML(ctx.nodesState.nodes[0], '{}');
assert(!rSheet.includes('nQuickStart'), '普通节点面板里没有临时隧道');
assert(el('#qcount').textContent.includes('1'), '临时隧道单独一栏');
assert(!el('#nodes').innerHTML.includes('a.trycloudflare.com') && el('#quicknodes').innerHTML.includes('a.trycloudflare.com'), '临时隧道不混在普通节点里');

// ---- 后端详情面板 ----
const sheet = ctx.nodeSheetHTML(ctx.nodesState.nodes[0], '{}');
assert(sheet.includes('修改端口') && sheet.includes('Reality 伪装域名'), '入站面板有端口与 Reality 区块');
assert(sheet.includes('data-cred="u1"'), '入站面板能换 UUID');
assert(!sheet.includes('CDN'), 'vless-reality 不该出现 CDN 区块');
const hy2Sheet = ctx.nodeSheetHTML(ctx.nodesState.nodes[1], '{}');
assert(hy2Sheet.includes('端口跳跃') && hy2Sheet.includes('Gecko 混淆'), 'hy2 面板有跳跃与混淆');
assert(!hy2Sheet.includes('固定隧道'), 'hysteria2 没有固定隧道区块');

// ---- 用户卡片 ----
ctx.subsState = { subs: [{
  name: 'alice', uuid: 'u-1', path: '/abc', sub_url: 'https://sub.example.com/abc',
  protocols: ['vless-reality-1'], links: ['vless://a@1:2#x'], has_sub: true, has_nginx: true,
  traffic_used: 1024 * 1024 * 100, limit_bytes: 1024 * 1024 * 1024, limit_on: true, speed_text: '10 MB/s',
}], nodes: [] };
ctx.renderSubs();
const subsHTML = el('#subs').innerHTML;
assert(subsHTML.includes('alice') && subsHTML.includes('sub.example.com/abc'), '用户卡片显示订阅地址');
assert(subsHTML.includes('vless-reality-1'), '用户卡片显示协议');
assert(subsHTML.includes('10 MB/s'), '用户卡片显示限速');

// ---- Nginx / 证书 / CF ----
ctx.infraState = {
  status: { installed: true, active: true, test_ok: true, version: 'nginx/1.24', conf_count: 2 },
  sub_domain: 'sub.example.com', sub_port: '443',
  sites: [{ name: 'app.conf', listen: ['443 ssl'], server_names: ['app.example.com'], proxy_pass: ['http://127.0.0.1:9393'], protected: false, user_site: false }],
  certs: [{ domain: 'node.example.com', cert_path: '/root/cert/node.example.com/fullchain.pem', key_path: '/k.pem', not_after: '2027-01-01 00:00', days: 400, self_signed: false, issuer: 'R3', used_by: ['入站 hysteria2-1.json'] }],
  zones: [{ name: 'example.com' }],
  cf: { configured: true, valid: true, hint: 'Token abcd…wxyz', kind: 'token', zones: [{ name: 'example.com' }], tunnels: [] },
};
ctx.renderNginx();
ctx.renderCerts();
ctx.renderCF();
assert(el('#ngxstatus').innerHTML.includes('nginx -t 通过'), 'nginx 状态徽章');
assert(el('#ngxsites').innerHTML.includes('app.example.com'), '站点列表显示域名');
assert(el('#certs').innerHTML.includes('剩 400 天'), '证书天数');
assert(el('#certs').innerHTML.includes('hysteria2-1.json'), '证书使用方');
assert(el('#cfbox').innerHTML.includes('Token abcd'), 'CF 凭证提示（脱敏）');

// ---- 系统 ----
ctx.sysState = {
  overview: { version: 'v1.3.0', singbox_version: '1.14.4', public_ip: '203.0.113.9', hostname: 'vps', node_count: 2, user_count: 1, conf_count: 5, firewall: 'drop' },
  services: [{ name: 'sing-box', label: 'sing-box', installed: true, active: true, enabled: true, init: 'systemd', uptime: '1天 02小时', pid: '123' }],
  firewall: { available: true, policy: 'drop', inbounds: [{ tag: 'vless-reality-1', port: 23456, tcp: true, udp: false, need: ['tcp'] }, { tag: 'hysteria2-1', port: 23457, tcp: false, udp: false, need: ['udp'] }] },
  outbounds: [{ tag: 'wireguard-out', type: 'wireguard', note: 'WARP 换出口' }, { tag: 'direct', type: 'direct', note: '' }],
};
ctx.renderSystem();
assert(el('#sysinfo').innerHTML.includes('1.14.4'), '系统信息显示 sing-box 版本');
assert(el('#services').innerHTML.includes('运行 1天 02小时'), '服务运行时长');
assert(el('#firewall').innerHTML.includes('未放行'), '端口未放行提示');
ctx.renderOplog([{ time: '01-02 03:04', action: '添加入站', detail: 'vless-ws', ok: true }]);
assert(el('#oplog').innerHTML.includes('添加入站'), '操作日志渲染');

// ---- 站点编辑器内容 ----
ctx.infraState.zones = [{ name: 'example.com' }];
ctx.fillZoneSelect('#ciZone');
// fillZoneSelect 用的是真实 DOM，这里桩里 el 会返回同一个对象
assert(true, 'fillZoneSelect 不抛错');
