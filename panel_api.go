package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Nginx、证书、Cloudflare 凭证的网页接口。

// ---- Nginx ----

func handleNginxStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	domain, port := nginxSubDomain()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     getNginxStatus(),
		"sub_domain": domain,
		"sub_port":   port,
		"has_sub":    pathExists(panelPaths().NginxSubConf),
	})
}

func handleNginxSites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": listNginxSites()})
}

func handleNginxSite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	userSite := r.URL.Query().Get("user") == "1"
	content, err := readNginxSite(name, userSite)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "content": content,
		"protected": isProtectedSite(name)})
}

func handleNginxSave(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Content  string `json:"content"`
		UserSite bool   `json:"user_site"`
	}
	if !c.decode(&body) {
		return
	}
	err := saveNginxSite(body.Name, body.Content, body.UserSite)
	recordOp("保存 Nginx 配置", body.Name, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"status": getNginxStatus()})
}

func handleNginxDelete(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Name     string `json:"name"`
		UserSite bool   `json:"user_site"`
	}
	if !c.decode(&body) {
		return
	}
	err := deleteNginxSite(body.Name, body.UserSite)
	recordOp("删除 Nginx 配置", body.Name, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"sites": listNginxSites()})
}

func handleNginxProxy(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var spec proxySpec
	if !c.decode(&spec) {
		return
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = strings.TrimPrefix(strings.TrimSpace(spec.Domain), "www.")
	}
	name = strings.NewReplacer(".", "-", " ", "-").Replace(name)
	if !strings.HasSuffix(name, ".conf") {
		name += ".conf"
	}
	if !validConfName(name) {
		c.fail(http.StatusBadRequest, "文件名只能用字母、数字、- _ 和 .conf：%s", name)
		return
	}
	content, err := buildProxyConf(spec)
	if err != nil {
		c.fail(http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := saveNginxSite(name, content, false); err != nil {
		recordOp("新增反代站点", name, err)
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	recordOp("新增反代站点", fmt.Sprintf("%s → %s", spec.Domain, spec.Target), nil)
	c.ok(map[string]any{"name": name, "content": content, "sites": listNginxSites(),
		"status": getNginxStatus()})
}

func handleNginxService(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if !c.decode(&body) {
		return
	}
	err := nginxService(body.Action)
	recordOp("nginx 服务", body.Action, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"status": getNginxStatus()})
}

func handleNginxLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	lines := 120
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := parseIntSafe(v); err == nil {
			lines = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": nginxLogs(lines)})
}

// ---- 证书 ----

func handleCerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		creds := loadCFCreds()
		out := map[string]any{
			"certs":         listCerts(),
			"cf_configured": creds != nil,
		}
		if creds != nil {
			out["cf_hint"] = creds.masked()
			if zones, err := cfZones(); err == nil {
				out["zones"] = zones
			} else {
				out["zones_error"] = safeErrText(err.Error())
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
	}
}

// certIssueMu 防止两个人同时点「申请证书」把 acme.sh 搅在一起。
var certIssueMu sync.Mutex

func handleCertIssue(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var spec certIssueSpec
	if !c.decode(&spec) {
		return
	}
	if !certIssueMu.TryLock() {
		c.fail(http.StatusOK, "正在申请中，请等上一次结束")
		return
	}
	defer certIssueMu.Unlock()
	item, logs, err := issueCert(spec)
	recordOp("申请证书", spec.Zone+"/"+spec.Mode, err)
	if err != nil {
		c.fail(http.StatusOK, "%s\n%s", safeErrText(err.Error()), logs)
		return
	}
	c.ok(map[string]any{"cert": item, "log": logs})
}

func handleCertDelete(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Domain    string `json:"domain"`
		DeleteDNS bool   `json:"delete_dns"`
	}
	if !c.decode(&body) {
		return
	}
	note, err := deleteCert(body.Domain, body.DeleteDNS)
	recordOp("删除证书", body.Domain, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"note": note})
}

// ---- Cloudflare ----

func handleCFStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	creds := loadCFCreds()
	out := map[string]any{"configured": creds != nil}
	if creds == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["kind"] = creds.Kind
	out["hint"] = creds.masked()
	if err := cfVerify(); err != nil {
		out["valid"] = false
		out["error"] = safeErrText(err.Error())
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["valid"] = true
	if zones, err := cfZones(); err == nil {
		out["zones"] = zones
	}
	if acct, err := cfAccountID(); err == nil {
		out["account"] = acct
		if tunnels, err := cfListTunnels(acct); err == nil {
			var brief []map[string]any
			for _, t := range tunnels {
				brief = append(brief, map[string]any{
					"id": t.ID, "name": t.Name, "status": t.Status,
					"connections": len(t.Connections),
				})
			}
			out["tunnels"] = brief
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCFCred 保存 / 删除 / 验证 Cloudflare 凭证。保存后立刻验证，失败就退回去。
func handleCFCred(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Action string `json:"action"` // save / delete
		Kind   string `json:"kind"`   // token / global
		Token  string `json:"token"`
		Email  string `json:"email"`
		Key    string `json:"key"`
	}
	if !c.decode(&body) {
		return
	}
	switch body.Action {
	case "delete":
		clearCFCreds()
		recordOp("Cloudflare 凭证", "已删除", nil)
		c.ok(nil)
		return
	case "save":
	default:
		c.fail(http.StatusBadRequest, "未知操作")
		return
	}
	old := loadCFCreds()
	creds := &cfCreds{Kind: body.Kind, Token: body.Token, Email: body.Email, Key: body.Key}
	if body.Kind == "" {
		creds.Kind = "token"
	}
	if err := saveCFCreds(creds); err != nil {
		c.fail(http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := cfVerify(); err != nil {
		// 验证不通过就把原凭证放回去，避免把能用的凭证换成不能用的
		if old != nil {
			_ = saveCFCreds(old)
		} else {
			clearCFCreds()
		}
		recordOp("Cloudflare 凭证", "验证失败", err)
		c.fail(http.StatusOK, "凭证验证没通过：%s", safeErrText(err.Error()))
		return
	}
	recordOp("Cloudflare 凭证", creds.masked(), nil)
	out := map[string]any{"hint": creds.masked(), "kind": creds.Kind}
	if zones, err := cfZones(); err == nil {
		out["zones"] = zones
	}
	c.ok(out)
}
