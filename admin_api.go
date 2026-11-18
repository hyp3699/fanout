package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 新增的网页面板接口（入站 / 用户 / Nginx / 证书 / 系统）共用的小工具。
// 所有路由都挂在 auth.Wrap 与 StripBasePath 之后，鉴权走原有会话。

const maxJSONBody = 2 << 20 // 2MB，配置文本再大也没这么大

// apiCtx 是一次请求的上下文，出错时统一返回 {"error": "..."}。
type apiCtx struct {
	w http.ResponseWriter
	r *http.Request
}

func newAPICtx(w http.ResponseWriter, r *http.Request) *apiCtx { return &apiCtx{w: w, r: r} }

func (c *apiCtx) fail(code int, format string, args ...any) {
	writeJSON(c.w, code, map[string]any{"error": fmt.Sprintf(format, args...)})
}

func (c *apiCtx) ok(v any) {
	if v == nil {
		v = map[string]any{"ok": true}
	}
	writeJSON(c.w, http.StatusOK, v)
}

// decode 读请求体里的 JSON。
func (c *apiCtx) decode(v any) bool {
	if c.r.Body == nil {
		c.fail(http.StatusBadRequest, "缺少请求内容")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.w, c.r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		c.fail(http.StatusBadRequest, "请求格式不对：%v", err)
		return false
	}
	return true
}

// needMethod 只接受 GET 或 POST。
func (c *apiCtx) needMethod(method string) bool {
	if c.r.Method != method {
		c.w.Header().Set("Allow", method)
		c.fail(http.StatusMethodNotAllowed, "只支持 %s", method)
		return false
	}
	return true
}

// ---- 操作日志 ----

// opEntry 是一次网页操作的记录，界面底部可以展开看。
type opEntry struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

var (
	opMu    sync.Mutex
	opRing  []opEntry
	opLimit = 200
)

// recordOp 记一条操作日志。detail 里绝不能放密码 / Token 原文。
func recordOp(action, detail string, err error) {
	e := opEntry{Time: time.Now().Format("01-02 15:04:05"), Action: action, Detail: detail, OK: err == nil}
	if err != nil {
		e.Error = err.Error()
	}
	opMu.Lock()
	opRing = append(opRing, e)
	if len(opRing) > opLimit {
		opRing = opRing[len(opRing)-opLimit:]
	}
	opMu.Unlock()
}

func recentOps() []opEntry {
	opMu.Lock()
	defer opMu.Unlock()
	out := make([]opEntry, len(opRing))
	copy(out, opRing)
	// 新的在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// opsSince 只取某个时间点之后的（界面轮询用）。
func opsSince(since string) []opEntry {
	all := recentOps()
	if since == "" {
		return all
	}
	var out []opEntry
	for _, e := range all {
		if e.Time > since {
			out = append(out, e)
		}
	}
	return out
}

// ---- 路由注册 ----

// registerAdminAPI 把新增面板的接口挂到 mux 上。
// 路径统一用 /api/ 前缀，跟原有接口一样走登录中间件。
func registerAdminAPI(mux *http.ServeMux) {
	// 入站
	mux.HandleFunc("/api/nodes", handleNodes)
	mux.HandleFunc("/api/nodes/create", handleNodeCreate)
	mux.HandleFunc("/api/nodes/detail", handleNodeDetail)
	mux.HandleFunc("/api/nodes/config", handleNodeConfig)
	mux.HandleFunc("/api/nodes/delete", handleNodeDelete)
	mux.HandleFunc("/api/nodes/port", handleNodePort)
	mux.HandleFunc("/api/nodes/cred", handleNodeCred)
	mux.HandleFunc("/api/nodes/reality", handleNodeReality)
	mux.HandleFunc("/api/nodes/hy2", handleNodeHy2)
	mux.HandleFunc("/api/nodes/cdn", handleNodeCDN)
	mux.HandleFunc("/api/nodes/tunnel", handleNodeTunnel)
	// 临时隧道
	mux.HandleFunc("/api/quick", handleQuickList)
	mux.HandleFunc("/api/quick/action", handleQuickAction)
	// 订阅用户
	mux.HandleFunc("/api/subs", handleSubs)
	mux.HandleFunc("/api/subs/add", handleSubAdd)
	mux.HandleFunc("/api/subs/delete", handleSubDelete)
	mux.HandleFunc("/api/subs/rebuild", handleSubRebuild)
	// Nginx
	mux.HandleFunc("/api/nginx/status", handleNginxStatus)
	mux.HandleFunc("/api/nginx/sites", handleNginxSites)
	mux.HandleFunc("/api/nginx/site", handleNginxSite)
	mux.HandleFunc("/api/nginx/save", handleNginxSave)
	mux.HandleFunc("/api/nginx/delete", handleNginxDelete)
	mux.HandleFunc("/api/nginx/proxy", handleNginxProxy)
	mux.HandleFunc("/api/nginx/service", handleNginxService)
	mux.HandleFunc("/api/nginx/logs", handleNginxLogs)
	// 证书与 Cloudflare
	mux.HandleFunc("/api/certs", handleCerts)
	mux.HandleFunc("/api/certs/issue", handleCertIssue)
	mux.HandleFunc("/api/certs/delete", handleCertDelete)
	mux.HandleFunc("/api/cf", handleCFStatus)
	mux.HandleFunc("/api/cf/cred", handleCFCred)
	// 系统
	mux.HandleFunc("/api/system", handleSystem)
	mux.HandleFunc("/api/system/service", handleSystemService)
	mux.HandleFunc("/api/system/logs", handleSystemLogs)
	mux.HandleFunc("/api/system/oplog", handleOPLog)
	mux.HandleFunc("/api/firewall", handleFirewall)
}

// safeErrText 去掉可能出现在错误里的敏感串（Token 之类）。
func safeErrText(s string) string {
	creds := loadCFCreds()
	if creds != nil {
		if creds.Token != "" {
			s = strings.ReplaceAll(s, creds.Token, "***")
		}
		if creds.Key != "" {
			s = strings.ReplaceAll(s, creds.Key, "***")
		}
	}
	return s
}
