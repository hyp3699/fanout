package main

import (
	"fmt"
	"net/http"
	"strings"
)

// 订阅用户相关的网页接口。

// handleSubs 列出订阅用户，顺带给出选中入站用的清单。
func handleSubs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	subs := listSubscribers()
	if subs == nil {
		subs = []subscriber{}
	}
	nodes, _ := listNodes()
	type nodeBrief struct {
		File  string `json:"file"`
		Tag   string `json:"tag"`
		Kind  string `json:"kind"`
		Port  int    `json:"port"`
		Users int    `json:"users"`
	}
	var briefs []nodeBrief
	for _, n := range nodes {
		briefs = append(briefs, nodeBrief{File: n.File, Tag: n.Tag, Kind: n.Kind,
			Port: n.Port, Users: len(n.Users)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subs": subs, "nodes": briefs, "sub_ready": subSiteReady()})
}

// handleSubAdd 添加用户：挂到选中的入站上，生成链接与订阅。
func handleSubAdd(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var req subAddRequest
	if !c.decode(&req) {
		return
	}
	subMu.Lock()
	sub, err := addSubscriber(req)
	subMu.Unlock()
	recordOp("添加用户", strings.Join(req.Files, ",")+" → "+req.Name, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"sub": sub})
}

// handleSubDelete 删用户。
func handleSubDelete(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Name         string `json:"name"`
		PreserveData bool   `json:"preserve_data"`
	}
	if !c.decode(&body) {
		return
	}
	subMu.Lock()
	err := deleteSubscriber(body.Name, body.PreserveData)
	subMu.Unlock()
	recordOp("删除用户", body.Name, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(nil)
}

// handleSubRebuild 重新把用户挂回全部入站（修「用户协议丢了」）。
func handleSubRebuild(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Reset string `json:"reset"` // "" 重建 / uuid / path
	}
	if !c.decode(&body) {
		return
	}
	op := "重建用户订阅"
	switch body.Reset {
	case "uuid":
		op = "重置用户 UUID"
	case "path":
		op = "重置订阅路径"
	case "":
	default:
		c.fail(http.StatusOK, "未知操作")
		return
	}
	subMu.Lock()
	sub, err := rebuildSubscriberWith(body.Name, body.Reset == "uuid", body.Reset == "path")
	subMu.Unlock()
	recordOp(op, body.Name, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"sub": sub})
}

// ---- 操作日志 ----

func handleOPLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ops": opsSince(r.URL.Query().Get("since"))})
}

// ---- 系统 ----

// handleSystem 系统页数据：概览、服务状态、可选出站、防火墙。
func handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	out := map[string]any{
		"overview":  systemOverviewInfo(),
		"services":  serviceStates(),
		"outbounds": foreignOutbounds(),
		"firewall":  firewallSnapshot(),
		"basepath":  currentBasePath(),
		"cf_global": pathExists(panelPaths().CFGlobalFile),
		"cf_token":  pathExists(panelPaths().CFTokenFile),
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSystemService 启停 / 重启 / 重载 sing-box 与 nginx。
func handleSystemService(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		Unit   string `json:"unit"`
		Action string `json:"action"`
	}
	if !c.decode(&body) {
		return
	}
	unit := strings.TrimSpace(body.Unit)
	switch unit {
	case singbox.Service, "nginx", "fanout":
	default:
		c.fail(http.StatusBadRequest, "只能操作 sing-box / nginx / fanout")
		return
	}
	err := controlService(unit, body.Action)
	recordOp("服务操作", unit+" "+body.Action, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	// 重启 sing-box 之后再确认一次配置没坏
	if unit == singbox.Service && (body.Action == "restart" || body.Action == "start") {
		if err := singboxCheck(); err != nil {
			c.fail(http.StatusOK, "%s", err.Error())
			return
		}
	}
	c.ok(map[string]any{"services": serviceStates()})
}

// handleSystemLogs 看服务日志。
func handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	unit := strings.TrimSpace(r.URL.Query().Get("unit"))
	lines := 120
	if v := strings.TrimSpace(r.URL.Query().Get("lines")); v != "" {
		if n, err := parseIntSafe(v); err == nil {
			lines = n
		}
	}
	switch unit {
	case "", singbox.Service, "nginx", "fanout":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不支持的服务"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": serviceLogs(unit, lines)})
}

// handleFirewall 看端口放行状态；POST {action:"sync"} 一键补齐。
func handleFirewall(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"firewall": firewallSnapshot()})
	case http.MethodPost:
		c := newAPICtx(w, r)
		var body struct {
			Action string `json:"action"`
			File   string `json:"file"`
		}
		if !c.decode(&body) {
			return
		}
		switch body.Action {
		case "sync":
			n, err := syncFirewall()
			recordOp("防火墙", fmt.Sprintf("补齐 %d 条放行", n), err)
			if err != nil {
				c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
				return
			}
			c.ok(map[string]any{"count": n, "firewall": firewallSnapshot()})
		case "allow":
			n, _, err := loadNode(body.File)
			if err != nil {
				c.fail(http.StatusOK, "%s", err.Error())
				return
			}
			proto := "tcp"
			if n.Kind == "hysteria2" || n.Kind == "tuic" {
				proto = "udp"
			}
			err = allowPort(n.Port, proto)
			recordOp("放行端口", fmt.Sprintf("%d/%s", n.Port, proto), err)
			if err != nil {
				c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
				return
			}
			c.ok(map[string]any{"firewall": firewallSnapshot()})
		default:
			c.fail(http.StatusBadRequest, "未知操作")
		}
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET / POST"})
	}
}

func parseIntSafe(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("不是数字")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
