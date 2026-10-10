package main

import "net/http"

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fanout</title>
<style>
:root{
  --bg:#12151a; --panel:#181c23; --line:#262c36; --text:#dde3ec;
  --dim:#8b95a5; --accent:#4a9eda; --ok:#3fa66b; --warn:#c9903a; --bad:#c25450;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
  font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
header{display:flex;align-items:center;gap:16px;padding:10px 16px;
  border-bottom:1px solid var(--line);background:var(--panel)}
h1{font-size:13px;font-weight:600;margin:0;letter-spacing:0}
.spacer{flex:1}
button{font:inherit;color:var(--text);background:#222833;border:1px solid var(--line);
  border-radius:4px;padding:4px 10px;cursor:pointer;display:inline-flex;
  align-items:center;gap:5px;white-space:nowrap}
button:hover:not(:disabled){border-color:var(--accent)}
button:disabled{opacity:.45;cursor:default}
button.primary{background:var(--accent);border-color:var(--accent);color:#0b0e12;font-weight:600}
button.icon{padding:3px 6px;background:transparent;border-color:transparent;color:var(--dim)}
button.icon:hover:not(:disabled){color:var(--accent);border-color:var(--line)}
button.icon.danger:hover:not(:disabled){color:var(--bad);border-color:rgba(194,84,80,.35)}
svg{width:14px;height:14px;stroke:currentColor;fill:none;stroke-width:1.8;
  stroke-linecap:round;stroke-linejoin:round;flex:none}
main{padding:14px 16px 40px;max-width:1180px;margin:0 auto}
.bar{display:flex;align-items:center;gap:10px;margin-bottom:12px}
.bar h2{font-size:12px;margin:0;font-weight:600;color:var(--dim)}
.exit{border:1px solid var(--line);border-radius:6px;margin-bottom:8px;
  background:var(--panel);overflow:hidden}
.exit>.row{display:grid;gap:6px 12px;align-items:center;padding:9px 12px;
  grid-template-columns:14px minmax(132px,auto) 1fr auto auto auto;
  grid-template-areas:"dot ip meta chips socks acts"}
.exit .dot{grid-area:dot}
.exit .ip{grid-area:ip}
.exit .meta{grid-area:meta}
.exit .chips{grid-area:chips}
.exit .socks{grid-area:socks}
.exit .acts{grid-area:acts}
.dot{width:8px;height:8px;border-radius:50%;background:var(--dim);justify-self:center}
.dot.up{background:var(--ok)}
.dot.starting{background:var(--warn);animation:pulse 1.2s ease-in-out infinite}
.dot.failed{background:var(--bad)}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
.ip{font-weight:600;font-variant-numeric:tabular-nums}
.meta{color:var(--dim);font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.chips{display:flex;gap:6px;flex-wrap:wrap}
.chip{border:1px solid var(--line);border-radius:3px;padding:1px 7px;font-size:11px;
  color:var(--dim);cursor:pointer;background:#0e1116}
.chip:hover{border-color:var(--accent);color:var(--text)}
.chip.none{border-style:dashed;cursor:default}
.chip.none:hover{border-color:var(--line);color:var(--dim)}
.orphan{margin-top:18px;border:1px solid var(--line);border-radius:6px;
  background:var(--panel);padding:10px 12px}
.orphan .top{display:flex;align-items:center;gap:10px;margin-bottom:8px}
.orphan .top h3{font-size:12px;margin:0;font-weight:600;color:var(--dim)}
.socks{color:var(--dim);font-size:12px;font-variant-numeric:tabular-nums}
.socks button{background:transparent;border-color:transparent;color:var(--dim);
  font-size:12px;padding:2px 6px;font-variant-numeric:tabular-nums}
.socks button:hover:not(:disabled){color:var(--accent);border-color:var(--line)}
.socks button .lock{width:11px;height:11px;stroke-width:2}
.acts{display:flex;gap:2px;justify-self:end}
.errline{padding:0 12px 9px 38px;color:var(--bad);font-size:11px;
  overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.empty{border:1px dashed var(--line);border-radius:6px;padding:40px 20px;
  text-align:center;color:var(--dim)}
.empty button{margin-top:14px}
.jobs{margin-bottom:12px}
.job{border:1px solid var(--line);border-radius:6px;background:var(--panel);
  padding:10px 12px;margin-bottom:8px}
.job .top{display:flex;align-items:center;gap:10px;margin-bottom:8px}
.job .top strong{font-weight:600;font-size:12px}
.steps{display:flex;flex-wrap:wrap;gap:6px}
.step{display:flex;align-items:center;gap:5px;font-size:11px;color:var(--dim);
  border:1px solid var(--line);border-radius:3px;padding:2px 7px;background:#0e1116}
.step.ok{color:var(--ok);border-color:rgba(63,166,107,.35)}
.step.failed{color:var(--bad);border-color:rgba(194,84,80,.35)}
.step.running{color:var(--warn);border-color:rgba(201,144,58,.35)}
.spin{animation:rot 1s linear infinite;transform-origin:center}
@keyframes rot{to{transform:rotate(360deg)}}
.links{display:flex;gap:14px;margin-right:4px}
.links a{color:var(--dim);text-decoration:none;font-size:12px}
.links a:hover{color:var(--accent)}
@media(max-width:820px){.links{display:none}
  main{padding:12px 12px 40px}
  .exit>.row{grid-template-columns:14px 1fr auto;
    grid-template-areas:"dot ip acts" ". meta meta" ". socks socks" ". chips chips"}
  .exit .chips{margin-top:2px}
  .bar{flex-wrap:wrap}}
.modal{position:fixed;inset:0;background:rgba(8,10,14,.72);display:none;
  align-items:center;justify-content:center;z-index:50;padding:20px}
.modal.open{display:flex}
.sheet{background:var(--bg);border:1px solid var(--line);border-radius:6px;
  width:min(680px,100%);max-height:86vh;display:flex;flex-direction:column}
.sheet .head{display:flex;align-items:center;gap:10px;padding:10px 14px;
  border-bottom:1px solid var(--line);background:var(--panel);border-radius:6px 6px 0 0}
.sheet .head h2{font-size:12px;margin:0;font-weight:600}
.sheet .body{overflow:auto;padding:14px}
.sheet .foot{display:flex;align-items:center;gap:10px;padding:10px 14px;
  border-top:1px solid var(--line);background:var(--panel);border-radius:0 0 6px 6px}
.count{color:var(--dim);font-size:11px}
label.f{display:block;margin-bottom:16px}
label.f[hidden]{display:none}
label.f>span{display:block;color:var(--dim);font-size:11px;margin-bottom:6px}
.regions{display:grid;grid-template-columns:repeat(auto-fill,minmax(148px,1fr));
  gap:6px;max-height:224px;overflow:auto}
.rg{border:1px solid var(--line);background:#0e1116;border-radius:4px;padding:7px 9px;
  cursor:pointer;text-align:left;display:block;width:100%}
.rg:hover{border-color:var(--accent)}
.rg.sel{border-color:var(--accent);background:rgba(74,158,218,.1)}
.rg b{font-weight:600;font-size:12px;display:block;overflow:hidden;
  text-overflow:ellipsis;white-space:nowrap}
.rg em{display:block;font-style:normal;color:var(--dim);font-size:11px;margin-top:2px}
.stepper{display:flex;align-items:center;gap:0;width:fit-content;
  border:1px solid var(--line);border-radius:4px;overflow:hidden;background:#0e1116}
.stepper button{border:0;border-radius:0;background:transparent;padding:5px 11px}
select,input[type=search],input[type=text]{font:inherit;background:#0e1116;
  border:1px solid var(--line);color:var(--text);border-radius:4px;
  padding:5px 8px;width:100%}
select:focus,input[type=search]:focus,input[type=text]:focus{outline:none;border-color:var(--accent)}
.stepper input[type=text]{width:56px;text-align:center;font:inherit;background:transparent;
  border:0;border-left:1px solid var(--line);border-right:1px solid var(--line);
  color:var(--text);padding:5px 0;font-variant-numeric:tabular-nums}
.stepper input:focus{outline:none}
.hint{color:var(--dim);font-size:11px;margin-top:6px}
.setrow{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:16px}
.setrow input,.setrow select{width:100%}
.updsec{margin-top:18px;padding-top:14px;border-top:1px solid var(--line)}
.updrow{display:flex;align-items:center;gap:10px}
.updver{font-size:12px;color:var(--text)}
.updver b{font-weight:600}
.updver span{color:var(--dim);margin-left:8px}
.updnotes{margin-top:10px;padding:10px;background:#0e1116;border:1px solid var(--line);
  border-radius:4px;font-size:12px;line-height:1.6;color:var(--dim);white-space:pre-wrap;
  max-height:180px;overflow:auto}
label.chk{display:flex;align-items:center;gap:7px;color:var(--text);font-size:12px;
  cursor:pointer;margin:0}
label.chk input{margin:0}
.hint.bad{color:var(--bad)}
.kv{display:grid;grid-template-columns:76px 1fr;gap:5px 12px;margin:0 0 14px}
.kv dt{color:var(--dim)}
.kv dd{margin:0;word-break:break-all}
.share{padding:10px;background:#0e1116;border:1px solid var(--line);
  border-radius:4px;word-break:break-all;font-size:12px;line-height:1.7;margin-bottom:8px}
.editbar{display:flex;align-items:flex-end;gap:12px;flex-wrap:wrap;
  padding:12px 0;border-top:1px solid var(--line);margin-top:4px}
.ef{display:block}
.ef>span{display:block;color:var(--dim);font-size:11px;margin-bottom:4px}
.ef input{width:150px}
.credrow{display:flex;align-items:flex-end;gap:12px;flex-wrap:wrap;margin-bottom:10px}
.credrow .ef input{width:190px}
.chead{display:flex;align-items:center;gap:10px;margin:14px 0 8px;
  padding-top:12px;border-top:1px solid var(--line)}
.chead h3{font-size:12px;margin:0;font-weight:600;color:var(--dim)}
.client{border:1px solid var(--line);border-radius:4px;padding:8px 10px;margin-bottom:8px}
.orow{display:flex;align-items:center;gap:10px;padding:6px 0}
.orow select{width:200px}
.crow{display:flex;align-items:center;gap:10px}
.cemail{font-weight:600;font-size:12px}
.cid{color:var(--dim);font-size:11px;overflow:hidden;text-overflow:ellipsis;
  white-space:nowrap;max-width:280px}
.client .share{margin:8px 0 0}
.share button{margin-top:8px}
.dim{color:var(--dim)}
.dot.off{background:var(--line)}
.empty.small{padding:18px 16px}
.chip.static{cursor:default}
.chip.static:hover{border-color:var(--line);color:var(--dim)}
.chip.miss{border-color:rgba(194,84,80,.5);color:var(--bad);text-decoration:line-through}
.rule{display:flex;align-items:center;gap:10px;flex-wrap:wrap;border:1px solid var(--line);
  border-radius:6px;background:var(--panel);padding:8px 12px;margin-bottom:6px}
.rule.off{opacity:.55}
.rule .rno{color:var(--dim);font-size:11px;min-width:18px;text-align:right}
.rule .rname{font-weight:600}
.rule .rcond{color:var(--dim);font-size:12px;flex:1;min-width:160px;overflow:hidden;
  text-overflow:ellipsis;white-space:nowrap}
.rule .rexit{font-size:12px;white-space:nowrap}
.rule .rexit.bad{color:var(--bad)}
.barhint{color:var(--dim);font-size:11px;margin:-6px 0 10px}
div.f{margin-bottom:16px}
.f>.lbl{display:block;color:var(--dim);font-size:11px;margin-bottom:6px}
.inchk{display:flex;flex-wrap:wrap;gap:6px 16px}
.rsrow{display:flex;gap:8px;align-items:center;margin-bottom:6px}
.rsrow input{flex:1}
.rsrow select{width:96px}
textarea{width:100%;min-height:300px;background:#0e1116;border:1px solid var(--line);
  color:var(--text);border-radius:4px;
  font:12px/1.8 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  padding:10px 12px;resize:vertical}
textarea:focus{outline:none;border-color:var(--accent)}
textarea.dom{min-height:120px}
.toast{position:fixed;left:50%;bottom:24px;transform:translateX(-50%);
  background:var(--panel);border:1px solid var(--line);border-radius:4px;
  padding:8px 14px;font-size:12px;z-index:80;opacity:0;pointer-events:none;
  transition:opacity .18s}
.toast.show{opacity:1}
.toast.bad{border-color:rgba(194,84,80,.5);color:var(--bad)}
</style>
</head>
<body>
<header>
  <h1>fanout</h1>
  <span class="count" id="panel"></span>
  <span class="spacer"></span>
  <button class="icon" id="settingsBtn" title="设置">
    <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
  </button>
  <nav class="links">
    <a href="https://t.me/+ft-zI76oovgwNmRh" target="_blank" rel="noopener">交流群</a>
    <a href="https://youtube.com/@joeyblog" target="_blank" rel="noopener">油管</a>
    <a href="https://joeyblog.net" target="_blank" rel="noopener">博客</a>
    <a href="https://github.com/hyp3699/fanout" target="_blank" rel="noopener">GitHub</a>
  </nav>
</header>

<main>
  <div class="jobs" id="jobs"></div>

  <div class="bar">
    <h2>出口</h2>
    <span class="count" id="ecount"></span>
    <span class="spacer"></span>
    <button id="exportAll" title="导出全部入站的分享链接">
      <svg viewBox="0 0 24 24"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5 5 5-5"/><path d="M12 15V3"/></svg>
      导出链接
    </button>
    <button id="stopall" title="停止所有出口">
      <svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>
      全部停止
    </button>
    <button class="primary" id="newexit">
      <svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
      新建出口
    </button>
  </div>

  <div id="list"></div>

  <div class="bar" style="margin-top:22px">
    <h2>分流规则</h2>
    <span class="count" id="rcount"></span>
    <span class="spacer"></span>
    <button class="primary" id="newrule">
      <svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
      新建规则
    </button>
  </div>
  <div class="barhint">按顺序匹配，先命中先生效。</div>
  <div id="rules"></div>

  <div class="bar" style="margin-top:22px">
    <h2>入站</h2>
    <span class="count" id="icount"></span>
  </div>
  <div id="inbounds"></div>
</main>

<div class="modal" id="wizard">
  <div class="sheet">
    <div class="head">
      <h2>新建出口</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="wizard" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="f">
        <span>地区</span>
        <input type="search" id="rgfilter" placeholder="筛选地区">
        <div class="regions" id="regions" style="margin-top:6px"></div>
      </label>
      <label class="f">
        <span id="countlabel">数量</span>
        <div class="stepper">
          <button id="minus" title="减少">
            <svg viewBox="0 0 24 24"><path d="M5 12h14"/></svg>
          </button>
          <input id="count" type="text" inputmode="numeric" value="3">
          <button id="plus" title="增加">
            <svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
          </button>
        </div>
        <div class="hint" id="availhint"></div>
      </label>
    </div>
    <div class="foot">
      <span class="count" id="wzhint"></span>
      <span class="spacer"></span>
      <button data-close="wizard">取消</button>
      <button class="primary" id="go">开始</button>
    </div>
  </div>
</div>

<div class="modal" id="detail">
  <div class="sheet">
    <div class="head">
      <h2 id="dtitle">入站</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="detail" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body" id="dbody"></div>
  </div>
</div>

<div class="modal" id="credbox">
  <div class="sheet">
    <div class="head">
      <h2>SOCKS5 访问凭据</h2>
      <span class="count" id="crtitle"></span>
      <span class="spacer"></span>
      <button class="icon" data-close="credbox" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <div class="share" id="crurl"></div>
      <div class="credrow">
        <label class="ef"><span>用户名</span>
          <input id="cruser" type="text" spellcheck="false"></label>
        <label class="ef"><span>口令</span>
          <input id="crpass" type="text" spellcheck="false"></label>
        <button id="crrand" title="随机生成一套">
          <svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></svg>
          随机
        </button>
      </div>
      <div class="hint">改完立即生效，已连上的会话不断；用旧凭据的客户端要改配置。</div>
    </div>
    <div class="foot">
      <button id="crcopy">
        <svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
        复制地址
      </button>
      <span class="spacer"></span>
      <button data-close="credbox">取消</button>
      <button class="primary" id="crsave">保存</button>
    </div>
  </div>
</div>

<div class="modal" id="export">
  <div class="sheet">
    <div class="head">
      <h2>分享链接</h2>
      <span class="count" id="excount"></span>
      <span class="spacer"></span>
      <button id="copyall">
        <svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
        全部复制
      </button>
      <button class="icon" data-close="export" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body"><textarea id="exbox" spellcheck="false" readonly></textarea></div>
  </div>
</div>

<div class="modal" id="rulebox">
  <div class="sheet">
    <div class="head">
      <h2 id="rtitle">新建分流规则</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="rulebox" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="f"><span>名称（可选）</span>
        <input id="rname" type="text" placeholder="比如：奈飞走日本"></label>
      <div class="f"><span class="lbl">用户（按入站里的用户名匹配，可多选）</span>
        <div class="inchk" id="rins"></div></div>
      <label class="f"><span>目标</span>
        <select id="rexit"></select>
        <div class="hint">fanout 出口没连通时这条规则暂不写进配置，连通后自动生效；已有出站的规则始终生效。</div></label>
      <label class="chk" style="margin-bottom:14px"><input type="checkbox" id="rall"> 全部流量：不看条件，所选用户的所有流量都走这个目标</label>
      <div id="rconds">
        <label class="f"><span>自定义域名</span>
          <textarea id="rdomains" class="dom" spellcheck="false" placeholder="netflix.com&#10;full:www.example.com&#10;keyword:nflx&#10;regex:^.+\.example\.org$&#10;203.0.113.0/24"></textarea>
          <div class="hint">一行一个，也可以用逗号或空格隔开。直接写 netflix.com 匹配它和所有子域；full: 只匹配这个域名；keyword: 关键字；regex: 正则；IP / CIDR 匹配目标地址。</div></label>
        <div class="f"><span class="lbl">已有规则集（可多选）</span>
          <div class="inchk" id="rlocal"></div></div>
        <div class="f"><span class="lbl">自定义规则集</span>
          <div id="rsets"></div>
          <button id="rsadd">
            <svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
            添加规则集
          </button>
          <div class="hint">geosite:netflix、geoip:jp 自动用 SagerNet 官方规则集；也可以填远程地址（.srs 是 binary，.json 是 source，看不出来就手动选）。保存时先下载校验一次，sing-box 之后每天自己更新。</div></div>
        <label class="chk"><input type="checkbox" id="rresolve"> 规则集里有 IP 段：先把域名解析成 IP 再匹配一遍</label>
        <div class="hint">填了 IP/CIDR、geoip:，或选了名字带 geoip 的已有规则集时会自动解析，不用勾。</div>
      </div>
      <label class="chk" style="margin-top:14px"><input type="checkbox" id="renabled" checked> 启用</label>
    </div>
    <div class="foot">
      <span class="count" id="rhint"></span>
      <span class="spacer"></span>
      <button data-close="rulebox">取消</button>
      <button class="primary" id="rsave">保存</button>
    </div>
  </div>
</div>

<div class="modal" id="settings">
  <div class="sheet">
    <div class="head">
      <h2>设置</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="settings" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="f"><span>访问口令</span>
        <input id="setPw" type="password" spellcheck="false" autocomplete="new-password" placeholder="留空则不改"></label>
      <div class="hint">改完只影响新登录，当前这个浏览器不会被踢下线。</div>

      <label class="f" style="margin-top:16px"><span>访问路径</span>
        <input id="setPath" type="text" spellcheck="false" placeholder="留空则去掉路径前缀"></label>
      <div class="hint" id="setPathHint">界面挂在这个路径下，扫端口的探不到。只能用字母数字和 - _。</div>

      <label class="f" style="margin-top:16px"><span>节点后端</span></label>
      <div class="hint" id="setBackendHint">sing-box：fanout 只写配置目录里的 fanout-outbounds.json 和 fanout-route.json。</div>

      <label class="chk" style="margin-top:16px"><input type="checkbox" id="setResi"> 只用家宽节点</label>
      <div class="hint" id="setResiHint">vpngate 里混着一批它自己的机房机器，出口一眼看得出是数据中心。勾着就只挑志愿者家宽。</div>

      <div class="setrow">
        <label class="f" style="margin:0"><span>监听端口</span>
          <input id="setPort" type="text" inputmode="numeric" spellcheck="false"></label>
        <label class="f" style="margin:0"><span>本地监听地址</span>
          <select id="setListen">
            <option value="0.0.0.0">所有网卡（0.0.0.0）</option>
            <option value="127.0.0.1">仅本机（127.0.0.1）</option>
          </select></label>
      </div>
      <div class="hint bad" id="setPortHint">改端口或监听地址会切换监听，保存后要用新地址重新打开界面。</div>

      <div class="updsec">
        <div class="updrow">
          <div class="updver">版本 <b id="updCur">-</b><span id="updLatest"></span></div>
          <span class="spacer"></span>
          <button id="updCheck">检查更新</button>
          <button class="primary" id="updApply" hidden>更新到 <span id="updApplyVer"></span></button>
        </div>
        <div class="updnotes" id="updNotes" hidden></div>
      </div>
    </div>
    <div class="foot">
      <span class="spacer"></span>
      <button data-close="settings">取消</button>
      <button class="primary" id="setSave">保存</button>
    </div>
  </div>
</div>

<div class="toast" id="toast"></div>

<script>
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

// ---- 入站（只读） ----
function renderInbounds(){
  const box = $('#inbounds');
  const list = view.inbounds || [];
  $('#icount').textContent = list.length ? list.length + ' 个' : '';
  if(!list.length){
    box.innerHTML = '<div class="empty small">配置目录里还没有入站。</div>';
    return;
  }
  box.innerHTML = '<div class="orphan"><div class="chips">' + list.map(i =>
      '<button class="chip" data-detail="' + i.id + '" title="来自 ' + esc(i.source || '') + '">'
      + esc(i.tag) + ' · ' + esc(i.protocol) + ' :' + i.port
      + (i.rules ? ' · ' + i.rules + ' 条规则' : '') + '</button>').join('')
    + '</div></div>';
}

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
    renderInbounds();
  }catch(e){}
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
</script>
</body>
</html>`
