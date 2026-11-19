package main

import (
	"net/http"
	"strings"

	_ "embed"
)

// 页面拆成四份嵌进二进制：基础样式、扩展样式、原有脚本、新标签页脚本。
// 拆开是为了能直接用 node --check 检查脚本语法（见 web/ 目录），
// 也给「不要在 main.go / web.go 里堆几千行」留个样子。
//
//go:embed web/app.css
var appCSS string

//go:embed web/admin.css
var adminCSS string

//go:embed web/app.js
var appJS string

//go:embed web/admin.js
var adminJS string

// indexHTML 是拼好的页面。{{CSS}} / {{JS}} 在启动时换掉。
var indexHTML = strings.NewReplacer(
	"{{CSS}}", appCSS,
	"{{CSS2}}", adminCSS,
	"{{JS}}", appJS,
	"{{JS2}}", adminJS,
).Replace(indexTemplate)

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

const indexTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fanout</title>
<style>{{CSS}}</style>
<style>{{CSS2}}</style>
</head>
<body>
<header>
  <h1>fanout</h1>
  <span class="count" id="panel"></span>
  <span class="spacer"></span>
  <button class="icon" id="logoutBtn" title="退出登录">
    <svg viewBox="0 0 24 24"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/></svg>
  </button>
  <button class="icon" id="settingsBtn" title="设置">
    <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
  </button>
</header>

<nav class="tabs" id="tabs">
  <button data-tab="tunnels" class="on">出口隧道</button>
  <button data-tab="nodes">节点入站</button>
  <button data-tab="subs">用户管理</button>
  <button data-tab="rules">分流规则</button>
  <button data-tab="infra">Nginx与证书</button>
  <button data-tab="system">系统</button>
</nav>

<main>
  <div class="jobs" id="jobs"></div>

  <section class="pane on" id="pane-tunnels">
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
  </section>

  <section class="pane" id="pane-nodes">
    <div class="bar">
      <h2>节点入站</h2>
      <span class="count" id="ncount"></span>
      <span class="spacer"></span>
      <button id="nodeRefresh">刷新</button>
      <button class="primary" id="nodeNew">添加入站</button>
    </div>
    <div id="nodes"></div>

    <div class="bar" style="margin-top:22px">
      <h2>CF 临时隧道</h2>
      <span class="count" id="qcount"></span>
      <span class="spacer"></span>
      <button class="primary" id="quickNew">新建临时隧道</button>
    </div>
    <div id="quicknodes"></div>
  </section>

  <section class="pane" id="pane-subs">
    <div class="bar">
      <h2>订阅用户</h2>
      <span class="count" id="scount"></span>
      <span class="spacer"></span>
      <button class="primary" id="subNew">添加用户</button>
    </div>
    <div id="subs"></div>

    <span id="ucount" hidden></span>
    <div id="users" hidden></div>
  </section>

  <section class="pane" id="pane-rules">
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
  </section>

  <section class="pane" id="pane-infra">
    <div class="bar">
      <h2>Nginx</h2>
      <span class="count" id="ngxcount"></span>
      <span class="spacer"></span>
      <button id="ngxTest">测试配置</button>
      <button id="ngxReload">重载</button>
      <button class="primary" id="ngxNew">反代站点</button>
    </div>
    <div id="ngxstatus"></div>
    <div id="ngxsites"></div>

    <div class="bar" style="margin-top:22px">
      <h2>证书</h2>
      <span class="count" id="certcount"></span>
      <span class="spacer"></span>
      <button class="primary" id="certNew">申请证书</button>
    </div>
    <div id="certs"></div>

    <div class="bar" style="margin-top:22px">
      <h2>Cloudflare</h2>
      <span class="spacer"></span>
      <button id="cfEdit">设置凭证</button>
    </div>
    <div id="cfbox"></div>
  </section>

  <section class="pane" id="pane-system">
    <div class="bar">
      <h2>系统</h2>
      <span class="count" id="sysver"></span>
      <span class="spacer"></span>
      <button id="sysRefresh">刷新</button>
    </div>
    <div id="sysinfo"></div>
    <div id="services"></div>

    <div class="bar" style="margin-top:22px">
      <h2>端口放行</h2>
      <span class="count" id="fwcount"></span>
      <span class="spacer"></span>
      <button id="fwSync">一键放行</button>
    </div>
    <div id="firewall"></div>


    <div class="bar" style="margin-top:22px">
      <h2>操作日志</h2>
      <span class="spacer"></span>
      <button id="opRefresh">刷新</button>
    </div>
    <div id="oplog"></div>
  </section>
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
        <select id="rexit"></select></label>
      <label class="chk" style="margin-bottom:14px"><input type="checkbox" id="rall"> 全部流量都走这个目标</label>
      <div id="rconds">
        <label class="f"><span>自定义域名</span>
          <textarea id="rdomains" class="dom" spellcheck="false" placeholder="netflix.com&#10;full:www.example.com&#10;keyword:nflx&#10;regex:^.+\.example\.org$&#10;203.0.113.0/24"></textarea>
</label>
        <div class="f"><span class="lbl">已有规则集（可多选）</span>
          <div class="inchk" id="rlocal"></div></div>
        <div class="f"><span class="lbl">自定义规则集</span>
          <div id="rsets"></div>
          <button id="rsadd">
            <svg viewBox="0 0 24 24"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
            添加规则集
          </button>
</div>
        <label class="chk"><input type="checkbox" id="rresolve"> 先解析域名再匹配 IP 段</label>
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

<div class="modal" id="userbox">
  <div class="sheet">
    <div class="head">
      <h2 id="utitle">用户</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="userbox" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <div class="usec" style="border-top:0;padding-top:0">
        <h3>限速</h3>
        <div class="hint" id="uspeedcur" style="margin-bottom:8px"></div>
        <div class="urow">
          <input id="uspeed" type="text" spellcheck="false" placeholder="单位 MB/s，例如 10 或 0.5；也可写 500KB、100Mbps">
          <select id="umode">
            <option value="bidirectional">上传+下载</option>
            <option value="upload">仅上传</option>
            <option value="download">仅下载</option>
          </select>
        </div>
        <div class="hint" id="uspeedcalc">1 MB/s = 8 Mbps：填 1，实际网速就是 8 Mbps</div>
        <div class="urow" style="margin-top:10px">
          <span class="spacer"></span>
          <button class="danger" id="uspeeddel">取消限速</button>
          <button class="primary" id="uspeedsave">保存限速</button>
        </div>
      </div>
      <div class="usec" style="margin-top:18px">
        <h3>流量限制</h3>
        <div class="hint" id="utrafcur" style="margin-bottom:8px"></div>
        <div class="urow">
          <input id="ulimit" type="text" spellcheck="false" placeholder="例如 2（=2GB）、500MB、1GB">
          <select id="uperiod">
            <option value="none">不重置</option>
            <option value="day">每天重置</option>
            <option value="month">每月重置</option>
          </select>
        </div>
        <div class="hint">达到限制后由流量统计服务自动停用该用户；保存后本周期流量清零。</div>
        <div class="urow" style="margin-top:10px">
          <span class="spacer"></span>
          <button class="danger" id="utrafdel">取消流量限制</button>
          <button class="primary" id="utrafsave">保存流量限制</button>
        </div>
      </div>
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
      <label class="f"><span>登录用户名</span>
        <input id="setUser" type="text" spellcheck="false" autocomplete="username" autocapitalize="off"></label>
      <label class="f" style="margin-top:12px"><span>访问口令（至少 8 位）</span>
        <input id="setPw" type="password" spellcheck="false" autocomplete="new-password" placeholder="留空则不改"></label>
      <div class="hint">改用户名或口令后，其他浏览器的登录会被踢下线，当前这个保留。</div>

      <div class="usec" style="margin-top:16px">
        <h3>两步验证 <span id="totpState"></span></h3>
        <div id="totpSetup" hidden>
          <div class="hint">在手机验证器（Google Authenticator、Microsoft Authenticator 等）里添加这个密钥，或在手机上直接点链接：</div>
          <div class="linkbox mono" id="totpSecret" style="margin:6px 0"></div>
          <a id="totpLink" href="#" class="mono" style="font-size:11px">在验证器里打开</a>
        </div>
        <div class="urow" style="margin-top:8px">
          <input id="totpCode" type="text" inputmode="numeric" maxlength="6" placeholder="6 位验证码" autocomplete="one-time-code">
          <button id="totpBegin">开启两步验证</button>
          <button class="primary" id="totpConfirm" hidden>确认开启</button>
          <button class="danger" id="totpOff" hidden>关闭两步验证</button>
        </div>
      </div>

      <div class="usec" style="margin-top:16px">
        <h3>登录会话 <span class="dim" id="sessCount"></span></h3>
        <div class="urow"><span class="spacer"></span><button id="sessKick">踢掉其他浏览器</button></div>
      </div>

      <label class="f" style="margin-top:16px"><span>访问路径</span>
        <input id="setPath" type="text" spellcheck="false" placeholder="留空则去掉路径前缀"></label>
      <div class="hint" id="setPathHint">界面挂在这个路径下，扫端口的探不到。只能用字母数字和 - _。</div>

      <label class="f" style="margin-top:16px"><span>节点后端</span></label>
      <div class="hint" id="setBackendHint" hidden></div>

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


<div class="modal" id="nodecreate">
  <div class="sheet">
    <div class="head">
      <h2>添加入站</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="nodecreate" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="field"><span>类型</span><select id="ncKind"></select></label>
      <label class="field" style="margin-top:12px"><span>端口（留空自动挑一个空闲端口）</span>
        <input id="ncPort" type="text" inputmode="numeric" placeholder="自动"></label>
      <div id="ncSNIWrap"><label class="field"><span>Reality 伪装域名</span><select id="ncSNI"></select></label>
      <label class="field"><span>自定义域名（填了就用它）</span><input id="ncSNICustom" type="text" spellcheck="false" autocapitalize="off" placeholder="www.example.com"></label></div>
      <div id="ncTLSWrap">
        <label class="field" id="ncDomainWrap"><span>证书域名</span><select id="ncDomain"></select></label>
        <label class="chk" id="ncAddTLSWrap" style="margin-bottom:12px"><input type="checkbox" id="ncAddTLS"> 给这个入站加 TLS（用上面选的证书）</label>
      </div>
    </div>
    <div class="foot">
      <span class="spacer"></span>
      <button id="ncCancel">取消</button>
      <button class="primary" id="ncGo">添加</button>
    </div>
  </div>
</div>

<div class="modal" id="nodesheet">
  <div class="sheet">
    <div class="head">
      <h2 id="nodetitle">入站</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="nodesheet" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body" id="nodebody"></div>
    <div class="foot">
      <span class="spacer"></span>
      <button data-close="nodesheet">关闭</button>
    </div>
  </div>
</div>

<div class="modal" id="subadd">
  <div class="sheet">
    <div class="head">
      <h2>添加用户</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="subadd" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="field"><span>用户名（留空自动 test-user-N）</span>
        <input id="saName" type="text" placeholder="test-user-1" spellcheck="false"></label>
      <label class="field"><span>UUID（留空随机生成）</span>
        <input id="saUUID" type="text" placeholder="留空即可" spellcheck="false"></label>
      <div class="field"><span>挂到哪些入站</span>
        <div class="checklist" id="saFiles"></div></div>
    </div>
    <div class="foot">
      <span class="spacer"></span>
      <button id="saCancel">取消</button>
      <button class="primary" id="saGo">创建</button>
    </div>
  </div>
</div>

<div class="modal" id="ngxsite">
  <div class="sheet">
    <div class="head">
      <h2 id="nstitle">Nginx 配置</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="ngxsite" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <input type="hidden" id="nsname"><input type="hidden" id="nsuser">
      <textarea id="nscontent" class="code" spellcheck="false"></textarea>
      <div class="hintline" id="nshint"></div>
    </div>
    <div class="foot">
      <button class="danger" id="nsdel">删除</button>
      <span class="spacer"></span>
      <button id="nsCancel">取消</button>
      <button class="primary" id="nssave">保存</button>
    </div>
  </div>
</div>

<div class="modal" id="pxnew">
  <div class="sheet">
    <div class="head">
      <h2>新增反向代理站点</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="pxnew" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="field"><span>域名 / IP（从已有证书里选）</span>
        <select id="pxDomain"></select></label>
      <div class="row2">
        <label class="field"><span>监听端口</span>
          <input id="pxPort" type="text" inputmode="numeric" value="443"></label>
        <label class="field"><span>反代目标</span>
          <input id="pxTarget" type="text" value="127.0.0.1:9393" spellcheck="false"></label>
      </div>
      <label class="chk" style="margin-bottom:10px"><input type="checkbox" id="pxTLS" checked> 开 HTTPS</label>
      <label class="chk" style="margin-bottom:12px"><input type="checkbox" id="pxWS"> 支持 WebSocket</label>
      <label class="field"><span>文件名（留空按域名生成）</span>
        <input id="pxName" type="text" placeholder="app-example-com" spellcheck="false"></label>
      <label class="field"><span>额外 location 指令（可选，一行一条）</span>
        <textarea id="pxExtra" class="code" style="min-height:80px" spellcheck="false"></textarea></label>
    </div>
    <div class="foot">
      <span class="spacer"></span>
      <button id="pxCancel">取消</button>
      <button class="primary" id="pxGo">创建</button>
    </div>
  </div>
</div>

<div class="modal" id="certnew">
  <div class="sheet">
    <div class="head">
      <h2>申请证书</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="certnew" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <label class="field"><span>模式</span>
          <select id="ciMode">
            <option value="ip">IP 证书（Let's Encrypt，自动续期）</option>
            <option value="root">根域名</option>
            <option value="sub" selected>子域名前缀</option>
            <option value="wildcard">泛域名</option>
            <option value="origin_ca">Cloudflare Origin CA（15 年）</option>
          </select></label>
      <label class="field" id="ciIPWrap"><span>IP</span><select id="ciIP"></select></label>
      <div id="ciCFWrap">
      <details class="cfonce" id="ciOnce"><summary>临时使用 Cloudflare 凭证（用完即删，不保存）</summary>
        <label class="field"><span>API Token</span><input id="ciTok" type="password" autocomplete="off" spellcheck="false" placeholder="留空则用已保存的凭证"></label>
        <div class="row2"><label class="field"><span>或：登录邮箱</span><input id="ciMail" type="text" autocomplete="off" spellcheck="false"></label>
        <label class="field"><span>Global API Key</span><input id="ciKey" type="password" autocomplete="off" spellcheck="false"></label></div>
        <button type="button" id="ciLoad">用这个凭证读取域名</button>
      </details>
      <label class="field"><span>Cloudflare 域名</span><select id="ciZone"></select></label>
      <div class="row2">
        <label class="field"><span>前缀（子域名时填）</span>
          <input id="ciPrefix" type="text" placeholder="node"></label>
      </div>
      <label class="chk" style="margin-bottom:12px"><input type="checkbox" id="ciWild"> Origin CA 申请泛域名</label>
      </div>
      <textarea id="ciLog" class="code" spellcheck="false" hidden></textarea>
    </div>
    <div class="foot">
      <span class="spacer"></span>
      <button id="ciCancel">取消</button>
      <button class="primary" id="ciGo">开始申请</button>
    </div>
  </div>
</div>

<div class="modal" id="cfcred">
  <div class="sheet">
    <div class="head">
      <h2>Cloudflare 凭证</h2>
      <span class="spacer"></span>
      <button class="icon" data-close="cfcred" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body">
      <div class="hintline" id="cfHint"></div>
      <label class="field" style="margin-top:10px"><span>认证方式</span>
        <select id="cfKind">
          <option value="token">API Token（推荐）</option>
          <option value="global">Global API Key（邮箱 + Key）</option>
        </select></label>
      <div id="cfTokenWrap">
        <label class="field"><span>API Token（保存后只显示前后四位）</span>
          <input id="cfToken" type="password" autocomplete="new-password" spellcheck="false"></label>
      </div>
      <div id="cfGlobalWrap">
        <label class="field"><span>Cloudflare 登录邮箱</span>
          <input id="cfEmail" type="text" spellcheck="false" autocomplete="off"></label>
        <label class="field"><span>Global API Key</span>
          <input id="cfKey" type="password" autocomplete="new-password" spellcheck="false"></label>
      </div>
    </div>
    <div class="foot">
      <button class="danger" id="cfClear">删除凭证</button>
      <span class="spacer"></span>
      <button id="cfCancel">取消</button>
      <button class="primary" id="cfSave">保存</button>
    </div>
  </div>
</div>

<div class="modal" id="logbox">
  <div class="sheet">
    <div class="head">
      <h2 id="logtitle">详情</h2>
      <span class="spacer"></span>
      <button id="logcopy">复制</button>
      <button class="icon" id="logclose" title="关闭">
        <svg viewBox="0 0 24 24"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
      </button>
    </div>
    <div class="body"><textarea id="logtext" class="code" spellcheck="false" readonly></textarea></div>
  </div>
</div>

<div class="toast" id="toast"></div>

<script>{{JS}}</script>
<script>{{JS2}}</script>
</body>
</html>`
