package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// 入站（节点）相关的网页接口。

// nodeKindOption 是「添加入站」下拉里的一个选项。
type nodeKindOption struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Proto string `json:"proto"`
	TLS   string `json:"tls"` // none / self / domain / optional / reality
	Net   string `json:"net"`
	Note  string `json:"note"`
}

var nodeKindLabels = map[string]struct{ label, note string }{
	"vless-reality": {"VLESS Reality", "最常用，抗封锁，默认伪装 www.iij.ad.jp"},
	"hysteria2":     {"Hysteria2", "UDP，弱网快；支持端口跳跃与 Gecko 混淆"},
	"tuic":          {"TUIC v5", "UDP，拥塞控制 bbr"},
	"http-reality":  {"HTTP Reality", "Reality + HTTP 传输"},
	"grpc-reality":  {"gRPC Reality", "Reality + gRPC"},
	"anytls":        {"AnyTLS", "TLS 填充，抗主动探测"},
	"socks5":        {"SOCKS5", "带用户名密码的本地代理入站"},
	"xhttp-reality": {"XHTTP Reality", "Reality + XHTTP"},
	"vless-xhttp":   {"VLESS XHTTP", "无 TLS 也能跑，可后面加 CDN / 隧道"},
	"xhttp-udp-tls": {"XHTTP UDP TLS", "需要域名证书，支持 CDN / 隧道"},
	"vless-tcp-tls": {"VLESS TCP TLS", "需要域名证书"},
	"naiveproxy":    {"Naiveproxy", "需要域名证书"},
	"vmess-ws":      {"VMess WS", "可加 CDN / 固定隧道 / 临时隧道"},
	"vless-ws":      {"VLESS WS", "可加 CDN / 固定隧道 / 临时隧道"},
}

func nodeKindOptions() []nodeKindOption {
	out := make([]nodeKindOption, 0, len(nodeKinds)+1)
	// 脚本「添加入站」的第 15 项：专用临时隧道（VMess WS 127.0.0.1:8101 + trycloudflare）
	out = append(out, nodeKindOption{Kind: quickKindAlias,
		Label: "CF 临时隧道（VMess WS）", Proto: "vmess", TLS: "none", Net: "tcp",
		Note: "建 127.0.0.1:8101 的入站并立刻开 trycloudflare 隧道，域名每次重启都会变"})
	for _, k := range nodeKinds {
		info := nodeKindInfos[k]
		meta := nodeKindLabels[k]
		out = append(out, nodeKindOption{Kind: k, Label: meta.label, Proto: info.Proto,
			TLS: info.TLS, Net: info.Net, Note: meta.note})
	}
	return out
}

// handleNodes 列出全部入站，顺带把表单要用的东西一次给全。
func handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	nodes, err := listNodes()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	if nodes == nil {
		nodes = []*node{}
	}
	// 证书域名给「需要证书」的入站类型用
	var certDomains []string
	for _, c := range listCerts() {
		if !c.SelfSign && c.Days > 0 {
			certDomains = append(certDomains, c.Domain)
		}
	}
	sort.Strings(certDomains)
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":         nodes,
		"kinds":         nodeKindOptions(),
		"reality_sites": realitySites,
		"cert_domains":  certDomains,
		"quick":         listQuickTunnels(),
		"public_ip":     hostPublicIP(),
	})
}

// handleNodeDetail 单个入站：完整配置 + 链接。
func handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	n, _, err := loadNode(file)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	cfg, _ := nodeConfigText(file)
	obfsPwd := ""
	if n.Kind == "hysteria2" {
		obfsPwd = hy2ObfsPassword(n)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node": n, "config": cfg, "obfs_password": obfsPwd,
	})
}

// handleNodeConfig 只取配置文本（「查看配置」）。
func handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	cfg, err := nodeConfigText(strings.TrimSpace(r.URL.Query().Get("file")))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

// handleNodeCreate 添加一个入站。
func handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var spec nodeCreateSpec
	if !c.decode(&spec) {
		return
	}
	n, err := createNode(spec)
	recordOp("添加入站", spec.Kind, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"node": n})
}

// handleNodeDelete 删除入站。
func handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File      string `json:"file"`
		RemoveCDN bool   `json:"remove_cdn"`
	}
	if !c.decode(&body) {
		return
	}
	err := deleteNode(body.File, body.RemoveCDN)
	recordOp("删除入站", body.File, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"ok": true})
}

// handleNodePort 改端口。
func handleNodePort(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File string `json:"file"`
		Port int    `json:"port"`
	}
	if !c.decode(&body) {
		return
	}
	n, err := setNodePort(body.File, body.Port)
	recordOp("修改端口", fmt.Sprintf("%s → %d", body.File, body.Port), err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"node": n})
}

// handleNodeCred 改 UUID / 密码（value 留空就随机）。
func handleNodeCred(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File  string `json:"file"`
		User  string `json:"user"`
		Value string `json:"value"`
	}
	if !c.decode(&body) {
		return
	}
	val, err := rotateNodeCred(body.File, body.User, body.Value)
	recordOp("修改凭据", body.File+" "+body.User, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"value": val})
}

// handleNodeReality 改 Reality 伪装域名。
func handleNodeReality(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File string `json:"file"`
		SNI  string `json:"sni"`
	}
	if !c.decode(&body) {
		return
	}
	err := setRealitySNI(body.File, body.SNI)
	recordOp("修改 Reality 域名", body.File+" → "+body.SNI, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"ok": true})
}

// handleNodeHy2 Hysteria2 端口跳跃 / 混淆。
func handleNodeHy2(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File     string `json:"file"`
		Action   string `json:"action"` // hop_on / hop_off / obfs_on / obfs_off
		MinPort  int    `json:"min_port"`
		MaxPort  int    `json:"max_port"`
		Password string `json:"password"`
	}
	if !c.decode(&body) {
		return
	}
	var err error
	var password string
	switch body.Action {
	case "hop_on":
		err = setHy2Hopping(body.File, body.MinPort, body.MaxPort)
		recordOp("Hysteria2 端口跳跃", fmt.Sprintf("%s %d-%d", body.File, body.MinPort, body.MaxPort), err)
	case "hop_off":
		err = disableHy2Hopping(body.File)
		recordOp("Hysteria2 端口跳跃", body.File+" 关闭", err)
	case "obfs_on":
		password, err = setHy2Obfs(body.File, true, body.Password)
		recordOp("Hysteria2 混淆", body.File+" 开启", err)
	case "obfs_off":
		_, err = setHy2Obfs(body.File, false, "")
		recordOp("Hysteria2 混淆", body.File+" 关闭", err)
	default:
		c.fail(http.StatusBadRequest, "未知操作 %q", body.Action)
		return
	}
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	resp := map[string]any{"ok": true}
	if password != "" {
		resp["password"] = password
	}
	c.ok(resp)
}

// handleNodeCDN 开 / 关 CDN。
func handleNodeCDN(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File   string `json:"file"`
		Action string `json:"action"` // enable / disable
		Zone   string `json:"zone"`
		Prefix string `json:"prefix"`
		Domain string `json:"domain"`
	}
	if !c.decode(&body) {
		return
	}
	switch body.Action {
	case "enable":
		domain, link, err := enableCDN(body.File, body.Zone, body.Prefix)
		recordOp("开启 CDN", body.File+" "+domain, err)
		if err != nil && domain == "" {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		resp := map[string]any{"domain": domain, "link": link}
		if err != nil {
			resp["warning"] = safeErrText(err.Error())
		}
		c.ok(resp)
	case "disable":
		err := disableCDN(body.File, body.Domain)
		recordOp("关闭 CDN", body.File+" "+body.Domain, err)
		if err != nil {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		c.ok(nil)
	default:
		c.fail(http.StatusBadRequest, "未知操作")
	}
}

// handleNodeTunnel 开固定隧道（Cloudflare Tunnel）。
func handleNodeTunnel(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File   string `json:"file"`
		Zone   string `json:"zone"`
		Prefix string `json:"prefix"`
	}
	if !c.decode(&body) {
		return
	}
	host, link, err := enableFixedTunnel(body.File, body.Zone, body.Prefix)
	recordOp("开启固定隧道", body.File+" "+host, err)
	if err != nil {
		c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
		return
	}
	c.ok(map[string]any{"domain": host, "link": link})
}

// handleQuickList 临时隧道列表。
func handleQuickList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quick": listQuickTunnels()})
}

// handleQuickAction 临时隧道的启停 / 换域名 / 重置凭据 / 切协议。
func handleQuickAction(w http.ResponseWriter, r *http.Request) {
	c := newAPICtx(w, r)
	if !c.needMethod(http.MethodPost) {
		return
	}
	var body struct {
		File   string `json:"file"`
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if !c.decode(&body) {
		return
	}
	switch body.Action {
	case "start", "reset_domain":
		n, err := quickStart(body.File)
		recordOp("临时隧道", body.File+" "+body.Action, err)
		if err != nil {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		c.ok(map[string]any{"node": n})
	case "stop":
		err := quickStop(body.File)
		recordOp("临时隧道", body.File+" 停止", err)
		if err != nil {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		c.ok(nil)
	case "reset_creds":
		id, path, err := quickResetCreds(body.File)
		recordOp("临时隧道", body.File+" 重置凭据", err)
		if err != nil {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		c.ok(map[string]any{"uuid": id, "path": path})
	case "switch":
		n, err := quickSwitchProtocol(body.File, body.Target)
		recordOp("临时隧道", body.File+" 切换协议", err)
		if err != nil {
			c.fail(http.StatusOK, "%s", safeErrText(err.Error()))
			return
		}
		c.ok(map[string]any{"node": n})
	case "probe":
		n, _, err := loadNode(body.File)
		if err != nil {
			c.fail(http.StatusOK, "%s", err.Error())
			return
		}
		unit := quickUnitName(n.Kind, n.Number)
		dom, active, _ := quickTunnelState(unit)
		c.ok(map[string]any{"domain": dom, "active": active, "reachable": quickTunnelProbe(dom)})
	default:
		c.fail(http.StatusBadRequest, "未知操作 %q", body.Action)
	}
}
